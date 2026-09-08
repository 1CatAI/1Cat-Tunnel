package client

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"tunnel/internal/common"
)

type Client struct {
	updateMu       sync.Mutex
	cfgMu          sync.RWMutex
	cfg            Config
	configRevision uint64

	initOnce sync.Once
	restart  chan struct{}

	sessionMu     sync.RWMutex
	session       *session
	sessionCancel context.CancelFunc

	runtimeMu          sync.RWMutex
	runtimeStatus      string
	runtimeError       string
	runtimeChangedAt   time.Time
	connectedAt        time.Time
	assignments        []common.AssignedTunnel
	assignmentsUpdated time.Time
	webCSRFToken       string
	webAccessToken     string
}

type session struct {
	cfg       Config
	hostname  string
	presetMap map[string]common.Preset

	writeMu     sync.Mutex
	controlConn net.Conn
	accessToken string
	tlsConfig   *tls.Config

	attachRetryMu   sync.Mutex
	nextAttachRetry time.Time

	onIssuedCredential func(string) error
	onConnected        func()
	onAssignments      func(common.AssignmentsMessage)

	blockMu       sync.Mutex
	blockRequests map[string]chan common.BlockIPResponse
}

type streamConnectionEvent struct {
	ConnectionID  string `json:"connection_id"`
	PresetName    string `json:"preset"`
	RemoteAddr    string `json:"remote_addr"`
	LocalAddr     string `json:"local_addr"`
	DurationMS    int64  `json:"duration_ms,omitempty"`
	BytesToPublic uint64 `json:"bytes_to_public,omitempty"`
	BytesToLocal  uint64 `json:"bytes_to_local,omitempty"`
}

const (
	attachConnectionAttempts = 4
	attachRetrySpacing       = 250 * time.Millisecond
)

func Run(ctx context.Context, cfg Config) error {
	client := &Client{cfg: cfg}
	return client.Run(ctx)
}

func RunManaged(ctx context.Context, cfg Config) error {
	client := &Client{cfg: cfg}
	return client.Run(ctx)
}

func (c *Client) Run(ctx context.Context) error {
	c.initialize()
	if err := c.startLocalWeb(ctx); err != nil {
		return err
	}

	hostname, err := os.Hostname()
	if err != nil {
		hostname = ""
	}

	for {
		if ctx.Err() != nil {
			return nil
		}
		c.drainRestartSignal()

		c.cfgMu.RLock()
		sessionConfig, revision := c.cfg, c.configRevision
		c.cfgMu.RUnlock()
		if err := validateConfig(sessionConfig); err != nil {
			c.setRuntimeState("等待配置", err.Error(), false)
			if !c.waitForRetry(ctx, time.Duration(sessionConfig.ReconnectIntervalSec)*time.Second) {
				return nil
			}
			continue
		}

		if strings.TrimSpace(hostname) == "" {
			hostname = sessionConfig.NodeName
		}
		presetMap := make(map[string]common.Preset, len(sessionConfig.Presets))
		for _, preset := range sessionConfig.Presets {
			presetMap[preset.Name] = preset
		}

		sessionCtx, cancel := context.WithCancel(ctx)
		sess := &session{
			cfg:                sessionConfig,
			hostname:           hostname,
			presetMap:          presetMap,
			accessToken:        sessionConfig.Token,
			onIssuedCredential: func(token string) error { return c.persistSessionCredential(sessionConfig, token) },
			onConnected:        func() { c.forRevision(revision, c.markConnected) },
			onAssignments:      func(msg common.AssignmentsMessage) { c.forRevision(revision, func() { c.updateAssignments(msg) }) },
			blockRequests:      make(map[string]chan common.BlockIPResponse),
		}
		c.updateMu.Lock()
		c.cfgMu.RLock()
		stale := revision != c.configRevision
		c.cfgMu.RUnlock()
		if !stale {
			c.setRuntimeState("正在连接", "", false)
			c.setSession(sess, cancel)
		}
		c.updateMu.Unlock()
		if stale {
			cancel()
			continue
		}
		runErr := sess.run(sessionCtx)
		configurationChanged := sessionCtx.Err() != nil && ctx.Err() == nil
		cancel()
		c.clearSession(sess)

		if runErr != nil && ctx.Err() == nil && !configurationChanged {
			log.Printf(userText("客户端连接已断开：%v", "client disconnected: %v"), runErr)
			c.setRuntimeState("等待重连", runErr.Error(), false)
		} else if configurationChanged {
			c.setRuntimeState("正在应用新配置", "", false)
		}

		if ctx.Err() != nil {
			return nil
		}
		if configurationChanged {
			continue
		}
		if !c.waitForRetry(ctx, time.Duration(sessionConfig.ReconnectIntervalSec)*time.Second) {
			return nil
		}
	}
}

func (c *Client) initialize() {
	c.initOnce.Do(func() {
		c.restart = make(chan struct{}, 1)
		c.runtimeStatus = "正在启动"
		c.runtimeChangedAt = time.Now()
	})
}

func (c *Client) waitForRetry(ctx context.Context, delay time.Duration) bool {
	if delay <= 0 {
		delay = time.Duration(defaultReconnectIntervalSec) * time.Second
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-c.restart:
		return true
	case <-timer.C:
		return true
	}
}

func (c *Client) drainRestartSignal() {
	select {
	case <-c.restart:
	default:
	}
}

func (c *Client) currentConfig() Config {
	c.cfgMu.RLock()
	defer c.cfgMu.RUnlock()
	return c.cfg
}

func (c *Client) persistIssuedCredential(accessToken string) error {
	return c.persistSessionCredential(c.currentConfig(), accessToken)
}

func (c *Client) forRevision(revision uint64, action func()) {
	c.cfgMu.RLock()
	defer c.cfgMu.RUnlock()
	if revision == c.configRevision {
		action()
	}
}

func (c *Client) persistSessionCredential(source Config, accessToken string) error {
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return errors.New(userText("服务器签发的访问 Token 为空", "issued access token is empty"))
	}

	c.updateMu.Lock()
	defer c.updateMu.Unlock()
	updated := c.currentConfig()
	// A late reply from the previous host must not replace the new host's identity.
	if !sameHostedServerConnection(source, updated) || source.NodeName != updated.NodeName || source.Token != updated.Token {
		return nil
	}
	updated.BootstrapToken = ""
	updated.EnrollmentPassword = ""
	updated.Token = accessToken

	if strings.TrimSpace(updated.configPath) == "" {
		return errors.New(userText("客户端配置路径不可用", "client config path is unavailable"))
	}
	if err := SaveConfig(updated.configPath, updated); err != nil {
		return err
	}
	c.cfgMu.Lock()
	c.cfg = updated
	c.cfgMu.Unlock()
	return nil
}

func (c *Client) setSession(sess *session, cancel context.CancelFunc) {
	c.sessionMu.Lock()
	c.session = sess
	c.sessionCancel = cancel
	c.sessionMu.Unlock()
}

func (c *Client) clearSession(sess *session) {
	c.sessionMu.Lock()
	if c.session == sess {
		c.session = nil
		c.sessionCancel = nil
	}
	c.sessionMu.Unlock()
}

func (c *Client) requestReconnect() {
	c.sessionMu.RLock()
	cancel := c.sessionCancel
	c.sessionMu.RUnlock()
	if cancel != nil {
		cancel()
	}
	select {
	case c.restart <- struct{}{}:
	default:
	}
}

func (c *Client) replaceConfig(cfg Config) error {
	c.updateMu.Lock()
	defer c.updateMu.Unlock()
	return c.replaceConfigLocked(cfg)
}

func (c *Client) replaceConfigLocked(cfg Config) error {
	if strings.TrimSpace(cfg.configPath) == "" {
		cfg.configPath = c.currentConfig().configPath
	}
	if strings.TrimSpace(cfg.configPath) == "" {
		return errors.New(userText("客户端配置路径不可用", "client config path is unavailable"))
	}
	if err := validateConfig(cfg); err != nil {
		return err
	}
	if err := SaveConfig(cfg.configPath, cfg); err != nil {
		return err
	}
	loaded, err := LoadManagedConfig(cfg.configPath)
	if err != nil {
		return err
	}
	c.cfgMu.Lock()
	c.cfg = loaded
	c.configRevision++
	c.cfgMu.Unlock()
	c.clearAssignments()
	c.setRuntimeState("正在应用新配置", "", false)
	c.requestReconnect()
	return nil
}

func (c *Client) setRuntimeState(status, message string, connected bool) {
	c.runtimeMu.Lock()
	c.runtimeStatus = status
	c.runtimeError = strings.TrimSpace(message)
	c.runtimeChangedAt = time.Now()
	if connected {
		if c.connectedAt.IsZero() {
			c.connectedAt = time.Now()
		}
	} else {
		c.connectedAt = time.Time{}
	}
	c.runtimeMu.Unlock()
}

func (c *Client) markConnected() {
	c.setRuntimeState("已连接", "", true)
}

func (c *Client) updateAssignments(msg common.AssignmentsMessage) {
	c.runtimeMu.Lock()
	c.assignments = append([]common.AssignedTunnel(nil), msg.Tunnels...)
	c.assignmentsUpdated = time.Unix(msg.UpdatedAt, 0)
	if msg.UpdatedAt <= 0 {
		c.assignmentsUpdated = time.Now()
	}
	c.runtimeMu.Unlock()
}

func (c *Client) clearAssignments() {
	c.runtimeMu.Lock()
	c.assignments = nil
	c.assignmentsUpdated = time.Time{}
	c.runtimeMu.Unlock()
}

func (c *Client) currentSession() *session {
	c.sessionMu.RLock()
	defer c.sessionMu.RUnlock()
	return c.session
}

func (s *session) run(ctx context.Context) error {
	tlsConfig, err := buildClientTLSConfig(s.cfg)
	if err != nil {
		return err
	}
	s.tlsConfig = tlsConfig

	conn, err := s.dialServer(ctx)
	if err != nil {
		return fmt.Errorf(userText("连接服务器 %s 失败：%w", "dial server %s failed: %w"), s.cfg.ServerAddr, err)
	}
	defer conn.Close()
	defer s.failPendingBlockRequests(userText("控制连接已关闭", "control connection closed"))

	s.controlConn = conn
	stopContextClose := context.AfterFunc(ctx, func() {
		_ = conn.Close()
	})
	defer stopContextClose()

	register := common.RegisterNodeRequest{
		Type:          common.MessageTypeRegisterNode,
		Token:         s.accessToken,
		NodeName:      s.cfg.NodeName,
		Hostname:      s.hostname,
		Platform:      runtime.GOOS,
		ClientVersion: Version,
		Presets:       s.cfg.Presets,
	}
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	if err := common.WriteMessage(conn, register); err != nil {
		return fmt.Errorf(userText("发送节点注册请求失败：%w", "send register_node failed: %w"), err)
	}

	reader := bufio.NewReader(conn)
	line, err := common.ReadMessageLine(reader)
	if err != nil {
		return fmt.Errorf(userText("读取节点注册响应失败：%w", "read register_node response failed: %w"), err)
	}

	var resp common.RegisterNodeResponse
	if err := json.Unmarshal(line, &resp); err != nil {
		return fmt.Errorf(userText("解析节点注册响应失败：%w", "decode register_node response failed: %w"), err)
	}
	if !resp.OK {
		if resp.Error == "" {
			resp.Error = userText("服务器拒绝了节点注册", "server rejected the node registration")
		}
		return errors.New(resp.Error)
	}
	if strings.TrimSpace(resp.AccessToken) != "" {
		s.accessToken = strings.TrimSpace(resp.AccessToken)
		s.cfg.Token = s.accessToken
		s.cfg.EnrollmentPassword = ""
		if s.onIssuedCredential != nil {
			if err := s.onIssuedCredential(s.accessToken); err != nil {
				log.Printf(userText("警告：节点 %q 已收到独立凭据，但无法保存：%v", "WARNING: node %q received a dedicated credential but could not persist it: %v"), s.cfg.NodeName, err)
			} else {
				log.Printf(userText("节点 %q 已安全保存独立凭据", "node %q persisted its dedicated credential"), s.cfg.NodeName)
			}
		}
	}
	_ = conn.SetDeadline(time.Time{})

	log.Printf(userText("节点 %q 已连接，正在提供 %d 个映射预设", "node %q connected, exposing %d preset(s)"), s.cfg.NodeName, len(s.cfg.Presets))
	if runtime.GOOS == "linux" {
		log.Printf("服务器已连接：%s，实际 IP:端口=%s，TLS=%t", s.cfg.ServerAddr, conn.RemoteAddr(), s.cfg.TLSEnabled)
	}
	if s.onConnected != nil {
		s.onConnected()
	}

	for {
		line, err := common.ReadMessageLine(reader)
		if err != nil {
			return fmt.Errorf(userText("读取控制消息失败：%w", "read control message failed: %w"), err)
		}

		var envelope common.Envelope
		if err := json.Unmarshal(line, &envelope); err != nil {
			log.Printf(userText("控制消息无效：%v", "invalid control message: %v"), err)
			continue
		}

		switch envelope.Type {
		case common.MessageTypeOpenConnection:
			var req common.OpenConnectionRequest
			if err := json.Unmarshal(line, &req); err != nil {
				log.Printf(userText("解析连接请求失败：%v", "decode open_connection failed: %v"), err)
				continue
			}
			go s.handleOpenConnection(ctx, req)
		case common.MessageTypeAssignments:
			var msg common.AssignmentsMessage
			if err := json.Unmarshal(line, &msg); err != nil {
				log.Printf(userText("解析公网映射分配失败：%v", "decode assignments failed: %v"), err)
				continue
			}
			s.logAssignments(msg)
			if s.onAssignments != nil {
				s.onAssignments(msg)
			}
		case common.MessageTypeBlockIPResponse:
			var msg common.BlockIPResponse
			if err := json.Unmarshal(line, &msg); err != nil {
				log.Printf(userText("解析 IP 屏蔽响应失败：%v", "decode block_ip_response failed: %v"), err)
				continue
			}
			s.resolveBlockRequest(msg)
		default:
			log.Printf(userText("收到未知控制消息 %q", "unknown control message %q"), envelope.Type)
		}
	}
}

func (s *session) handleOpenConnection(ctx context.Context, req common.OpenConnectionRequest) {
	preset, ok := s.presetMap[req.PresetName]
	if !ok {
		_ = s.sendConnectionError(req, fmt.Sprintf(userText("此节点未配置映射预设 %q", "preset %q is not configured on this node"), req.PresetName))
		return
	}

	preset.Protocol = common.NormalizeProtocol(preset.Protocol)
	requestedProtocol := common.NormalizeProtocol(req.Protocol)
	if requestedProtocol == "" {
		requestedProtocol = preset.Protocol
	}
	if !common.IsSupportedProtocol(requestedProtocol) {
		_ = s.sendConnectionError(req, fmt.Sprintf(userText("不支持协议 %q", "unsupported protocol %q"), req.Protocol))
		return
	}
	if requestedProtocol != preset.Protocol {
		_ = s.sendConnectionError(req, fmt.Sprintf(userText("映射预设 %q 只允许 %s，但服务器请求了 %s", "preset %q only allows %s, but server requested %s"), req.PresetName, preset.Protocol, requestedProtocol))
		return
	}

	switch requestedProtocol {
	case common.NetworkUDP:
		s.handleOpenDatagramConnection(ctx, req, preset)
	default:
		s.handleOpenStreamConnection(ctx, req, preset)
	}
}

func (s *session) handleOpenStreamConnection(ctx context.Context, req common.OpenConnectionRequest, preset common.Preset) {
	localDialer := &net.Dialer{Timeout: 10 * time.Second}
	localConn, err := localDialer.DialContext(ctx, "tcp", preset.LocalAddr)
	if err != nil {
		_ = s.sendConnectionError(req, fmt.Sprintf(userText("连接本地服务 %s 失败：%v", "dial local %s failed: %v"), preset.LocalAddr, err))
		return
	}

	dataConn, err := s.openAttachConnection(ctx, req, common.NetworkTCP)
	if err != nil {
		_ = localConn.Close()
		_ = s.sendConnectionError(req, fmt.Sprintf(userText("连接数据通道失败：%v", "dial data channel failed: %v"), err))
		return
	}

	startedAt := time.Now()
	event := streamConnectionEvent{
		ConnectionID: req.ConnID,
		PresetName:   req.PresetName,
		RemoteAddr:   req.RemoteAddr,
		LocalAddr:    preset.LocalAddr,
	}
	logStreamConnectionEvent("OPEN", event)

	var bytesToPublic atomic.Uint64
	var bytesToLocal atomic.Uint64
	stopCancellation := closeDataOnCancel(ctx, localConn, dataConn)
	defer stopCancellation()
	common.ProxyWithAccounting(
		localConn,
		dataConn,
		func(written uint64) { bytesToPublic.Add(written) },
		func(written uint64) { bytesToLocal.Add(written) },
	)

	event.DurationMS = time.Since(startedAt).Milliseconds()
	event.BytesToPublic = bytesToPublic.Load()
	event.BytesToLocal = bytesToLocal.Load()
	logStreamConnectionEvent("CLOSE", event)
}

func logStreamConnectionEvent(state string, event streamConnectionEvent) {
	payload, err := json.Marshal(event)
	if err != nil {
		log.Printf("TCP %s connection_id=%q preset=%q remote=%q local=%q", state, event.ConnectionID, event.PresetName, event.RemoteAddr, event.LocalAddr)
		return
	}
	log.Printf("TCP %s %s", state, payload)
}

func (s *session) handleOpenDatagramConnection(ctx context.Context, req common.OpenConnectionRequest, preset common.Preset) {
	localDialer := &net.Dialer{Timeout: 10 * time.Second}
	localConn, err := localDialer.DialContext(ctx, "udp", preset.LocalAddr)
	if err != nil {
		_ = s.sendConnectionError(req, fmt.Sprintf(userText("连接本地服务 %s 失败：%v", "dial local %s failed: %v"), preset.LocalAddr, err))
		return
	}

	dataConn, err := s.openAttachConnection(ctx, req, common.NetworkUDP)
	if err != nil {
		_ = localConn.Close()
		_ = s.sendConnectionError(req, fmt.Sprintf(userText("连接 UDP 数据通道失败：%v", "dial UDP data channel failed: %v"), err))
		return
	}

	log.Printf(userText("已接受 %s 会话：映射=%s，来源=%s", "accepted %s session for preset=%s from %s"), strings.ToUpper(common.NetworkUDP), req.PresetName, req.RemoteAddr)
	stopCancellation := closeDataOnCancel(ctx, localConn, dataConn)
	defer stopCancellation()
	common.ProxyDatagrams(localConn, dataConn, nil, nil)
}

// Data channels belong to the control session, including during hot reloads.
func closeDataOnCancel(ctx context.Context, connections ...net.Conn) func() bool {
	return context.AfterFunc(ctx, func() {
		for _, connection := range connections {
			_ = connection.Close()
		}
	})
}

func (s *session) openAttachConnection(ctx context.Context, req common.OpenConnectionRequest, protocol string) (net.Conn, error) {
	return s.openAttachConnectionWithPolicy(
		ctx,
		req,
		protocol,
		s.dialServer,
		attachConnectionAttempts,
		attachRetrySpacing,
	)
}

func (s *session) openAttachConnectionWithPolicy(
	ctx context.Context,
	req common.OpenConnectionRequest,
	protocol string,
	dial func(context.Context) (net.Conn, error),
	attempts int,
	retrySpacing time.Duration,
) (net.Conn, error) {
	if attempts < 1 {
		attempts = 1
	}

	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if attempt > 1 {
			if err := s.waitForAttachRetry(ctx, retrySpacing); err != nil {
				return nil, err
			}
		}

		dataConn, err := s.openAttachConnectionOnce(ctx, req, protocol, dial)
		if err == nil {
			return dataConn, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if attempt < attempts {
			log.Printf(
				userText("数据通道附加第 %d/%d 次失败，连接标识=%s；正在重试：%v", "data attach attempt %d/%d failed for conn_id=%s; retrying: %v"),
				attempt,
				attempts,
				req.ConnID,
				err,
			)
		}
	}

	return nil, fmt.Errorf(userText("数据通道附加在 %d 次尝试后仍失败：%w", "data attach failed after %d attempts: %w"), attempts, lastErr)
}

func (s *session) openAttachConnectionOnce(
	ctx context.Context,
	req common.OpenConnectionRequest,
	protocol string,
	dial func(context.Context) (net.Conn, error),
) (net.Conn, error) {
	dataConn, err := dial(ctx)
	if err != nil {
		return nil, err
	}

	attach := common.AttachRequest{
		Type:     common.MessageTypeAttach,
		Token:    s.accessToken,
		TunnelID: req.TunnelID,
		ConnID:   req.ConnID,
		NodeName: s.cfg.NodeName,
		Protocol: protocol,
	}
	_ = dataConn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err := common.WriteMessage(dataConn, attach); err != nil {
		_ = dataConn.Close()
		return nil, fmt.Errorf(userText("发送数据通道附加请求失败：%w", "send attach failed: %w"), err)
	}
	if err := dataConn.SetWriteDeadline(time.Time{}); err != nil {
		_ = dataConn.Close()
		return nil, fmt.Errorf(userText("清除数据通道附加写入超时失败：%w", "clear attach write deadline: %w"), err)
	}

	return dataConn, nil
}

func (s *session) waitForAttachRetry(ctx context.Context, spacing time.Duration) error {
	if spacing <= 0 {
		return nil
	}

	now := time.Now()
	readyAt := now.Add(spacing)

	s.attachRetryMu.Lock()
	if s.nextAttachRetry.After(readyAt) {
		readyAt = s.nextAttachRetry
	}
	s.nextAttachRetry = readyAt.Add(spacing)
	s.attachRetryMu.Unlock()

	timer := time.NewTimer(time.Until(readyAt))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (s *session) dialServer(ctx context.Context) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	if s.tlsConfig == nil {
		return dialer.DialContext(ctx, "tcp", s.cfg.ServerAddr)
	}
	tlsDialer := &tls.Dialer{
		NetDialer: dialer,
		Config:    s.tlsConfig,
	}
	return tlsDialer.DialContext(ctx, "tcp", s.cfg.ServerAddr)
}

func buildClientTLSConfig(cfg Config) (*tls.Config, error) {
	if !cfg.TLSEnabled {
		return nil, nil
	}

	serverName := strings.TrimSpace(cfg.TLSServerName)
	if serverName == "" {
		host, _, err := net.SplitHostPort(strings.TrimSpace(cfg.ServerAddr))
		if err != nil {
			return nil, fmt.Errorf(userText("解析 TLS 服务器名称失败：%w", "resolve TLS server name: %w"), err)
		}
		serverName = strings.Trim(host, "[]")
	}

	roots, err := x509.SystemCertPool()
	if err != nil || roots == nil {
		roots = x509.NewCertPool()
	}
	if !roots.AppendCertsFromPEM(projectCAPEM) {
		return nil, errors.New(userText("内置的项目 TLS CA 无效", "embedded project TLS CA is invalid"))
	}

	if strings.TrimSpace(cfg.TLSCAFile) != "" {
		caPath := cfg.TLSCAFile
		if !filepath.IsAbs(caPath) && strings.TrimSpace(cfg.configPath) != "" {
			caPath = filepath.Join(filepath.Dir(cfg.configPath), caPath)
		}
		pemData, err := os.ReadFile(caPath)
		if err != nil {
			return nil, fmt.Errorf(userText("读取 TLS CA 文件失败：%w", "read TLS CA file: %w"), err)
		}
		if !roots.AppendCertsFromPEM(pemData) {
			return nil, errors.New(userText("TLS CA 文件不包含有效证书", "TLS CA file does not contain a valid certificate"))
		}
	}

	return &tls.Config{
		MinVersion: tls.VersionTLS13,
		ServerName: serverName,
		RootCAs:    roots,
	}, nil
}

func (s *session) sendConnectionError(req common.OpenConnectionRequest, message string) error {
	return s.sendControl(common.ConnectionError{
		Type:     common.MessageTypeConnectionError,
		TunnelID: req.TunnelID,
		ConnID:   req.ConnID,
		Error:    message,
	})
}

func (s *session) submitBlockIP(ctx context.Context, username, password, ip, reason string) (common.BlockIPResponse, error) {
	reqID := common.NewID("block")
	ch := make(chan common.BlockIPResponse, 1)

	s.blockMu.Lock()
	if s.blockRequests == nil {
		s.blockRequests = make(map[string]chan common.BlockIPResponse)
	}
	s.blockRequests[reqID] = ch
	s.blockMu.Unlock()

	req := common.BlockIPRequest{
		Type:          common.MessageTypeBlockIP,
		RequestID:     reqID,
		AdminUsername: username,
		AdminPassword: password,
		IP:            ip,
		Reason:        reason,
	}
	if err := s.sendControl(req); err != nil {
		s.removeBlockRequest(reqID)
		return common.BlockIPResponse{}, err
	}

	select {
	case resp := <-ch:
		if !resp.OK {
			if strings.TrimSpace(resp.Error) == "" {
				resp.Error = userText("服务器拒绝了 IP 屏蔽请求", "server rejected the block request")
			}
			return resp, errors.New(resp.Error)
		}
		return resp, nil
	case <-ctx.Done():
		s.removeBlockRequest(reqID)
		return common.BlockIPResponse{}, ctx.Err()
	}
}

func (s *session) resolveBlockRequest(resp common.BlockIPResponse) {
	reqID := strings.TrimSpace(resp.RequestID)
	if reqID == "" {
		return
	}

	s.blockMu.Lock()
	ch := s.blockRequests[reqID]
	delete(s.blockRequests, reqID)
	s.blockMu.Unlock()

	if ch != nil {
		ch <- resp
	}
}

func (s *session) removeBlockRequest(reqID string) {
	s.blockMu.Lock()
	delete(s.blockRequests, reqID)
	s.blockMu.Unlock()
}

func (s *session) failPendingBlockRequests(message string) {
	s.blockMu.Lock()
	pending := s.blockRequests
	s.blockRequests = make(map[string]chan common.BlockIPResponse)
	s.blockMu.Unlock()

	for reqID, ch := range pending {
		ch <- common.BlockIPResponse{
			Type:      common.MessageTypeBlockIPResponse,
			RequestID: reqID,
			Error:     message,
		}
	}
}

func (s *session) sendControl(message any) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	if s.controlConn == nil {
		return errors.New(userText("控制连接不可用", "control connection is not available"))
	}

	_ = s.controlConn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	err := common.WriteMessage(s.controlConn, message)
	clearErr := s.controlConn.SetWriteDeadline(time.Time{})
	if err != nil {
		return err
	}
	return clearErr
}

func (s *session) logAssignments(msg common.AssignmentsMessage) {
	if len(msg.Tunnels) == 0 {
		log.Printf(userText("服务器尚未为节点 %q 分配公网入口", "server has not assigned any public endpoints to node %q yet"), s.cfg.NodeName)
		return
	}

	log.Printf(userText("服务器发布了 %d 个公网入口，节点=%q", "server published %d public endpoint(s) for node %q"), len(msg.Tunnels), s.cfg.NodeName)
	for _, tunnel := range msg.Tunnels {
		line := fmt.Sprintf("preset=%s protocol=%s local=%s public=%s", strings.ToUpper(tunnel.PresetName), strings.ToUpper(common.NormalizeProtocol(tunnel.Protocol)), tunnel.LocalAddr, tunnel.PublicTarget)
		if strings.TrimSpace(tunnel.CommandHint) != "" {
			line += " hint=\"" + tunnel.CommandHint + "\""
		}
		log.Print(line)
	}
}
