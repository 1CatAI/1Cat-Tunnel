package client

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"tunnel/internal/common"
)

type Client struct {
	cfg Config
}

type session struct {
	cfg       Config
	hostname  string
	presetMap map[string]common.Preset

	writeMu     sync.Mutex
	controlConn net.Conn
}

func Run(ctx context.Context, cfg Config) error {
	client := &Client{cfg: cfg}
	return client.Run(ctx)
}

func (c *Client) Run(ctx context.Context) error {
	retryDelay := time.Duration(c.cfg.ReconnectIntervalSec) * time.Second

	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		hostname = c.cfg.NodeName
	}

	presetMap := make(map[string]common.Preset, len(c.cfg.Presets))
	for _, preset := range c.cfg.Presets {
		presetMap[preset.Name] = preset
	}

	for {
		if ctx.Err() != nil {
			return nil
		}

		sess := &session{
			cfg:       c.cfg,
			hostname:  hostname,
			presetMap: presetMap,
		}
		if err := sess.run(ctx); err != nil && ctx.Err() == nil {
			log.Printf("client disconnected: %v", err)
		}

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(retryDelay):
		}
	}
}

func (s *session) run(ctx context.Context) error {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", s.cfg.ServerAddr)
	if err != nil {
		return fmt.Errorf("dial server %s failed: %w", s.cfg.ServerAddr, err)
	}
	defer conn.Close()

	s.controlConn = conn
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

	register := common.RegisterNodeRequest{
		Type:     common.MessageTypeRegisterNode,
		Token:    s.cfg.Token,
		NodeName: s.cfg.NodeName,
		Hostname: s.hostname,
		Platform: runtime.GOOS,
		Presets:  s.cfg.Presets,
	}
	if err := common.WriteMessage(conn, register); err != nil {
		return fmt.Errorf("send register_node failed: %w", err)
	}

	reader := bufio.NewReader(conn)
	line, err := common.ReadMessageLine(reader)
	if err != nil {
		return fmt.Errorf("read register_node response failed: %w", err)
	}

	var resp common.RegisterNodeResponse
	if err := json.Unmarshal(line, &resp); err != nil {
		return fmt.Errorf("decode register_node response failed: %w", err)
	}
	if !resp.OK {
		if resp.Error == "" {
			resp.Error = "server rejected the node registration"
		}
		return errors.New(resp.Error)
	}

	log.Printf("node %q connected, exposing %d preset(s)", s.cfg.NodeName, len(s.cfg.Presets))

	for {
		line, err := common.ReadMessageLine(reader)
		if err != nil {
			return fmt.Errorf("read control message failed: %w", err)
		}

		var envelope common.Envelope
		if err := json.Unmarshal(line, &envelope); err != nil {
			log.Printf("invalid control message: %v", err)
			continue
		}

		switch envelope.Type {
		case common.MessageTypeOpenConnection:
			var req common.OpenConnectionRequest
			if err := json.Unmarshal(line, &req); err != nil {
				log.Printf("decode open_connection failed: %v", err)
				continue
			}
			go s.handleOpenConnection(ctx, req)
		case common.MessageTypeAssignments:
			var msg common.AssignmentsMessage
			if err := json.Unmarshal(line, &msg); err != nil {
				log.Printf("decode assignments failed: %v", err)
				continue
			}
			s.logAssignments(msg)
		default:
			log.Printf("unknown control message %q", envelope.Type)
		}
	}
}

func (s *session) handleOpenConnection(ctx context.Context, req common.OpenConnectionRequest) {
	preset, ok := s.presetMap[req.PresetName]
	if !ok {
		_ = s.sendConnectionError(req, fmt.Sprintf("preset %q is not configured on this node", req.PresetName))
		return
	}

	preset.Protocol = common.NormalizeProtocol(preset.Protocol)
	requestedProtocol := common.NormalizeProtocol(req.Protocol)
	if requestedProtocol == "" {
		requestedProtocol = preset.Protocol
	}
	if !common.IsSupportedProtocol(requestedProtocol) {
		_ = s.sendConnectionError(req, fmt.Sprintf("unsupported protocol %q", req.Protocol))
		return
	}
	if requestedProtocol != preset.Protocol {
		_ = s.sendConnectionError(req, fmt.Sprintf("preset %q only allows %s, but server requested %s", req.PresetName, preset.Protocol, requestedProtocol))
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
		_ = s.sendConnectionError(req, fmt.Sprintf("dial local %s failed: %v", preset.LocalAddr, err))
		return
	}

	dataConn, err := s.openAttachConnection(ctx, req, common.NetworkTCP)
	if err != nil {
		_ = localConn.Close()
		_ = s.sendConnectionError(req, fmt.Sprintf("dial data channel failed: %v", err))
		return
	}

	log.Printf("accepted %s connection for preset=%s from %s", strings.ToUpper(common.NetworkTCP), req.PresetName, req.RemoteAddr)
	common.Proxy(localConn, dataConn)
}

func (s *session) handleOpenDatagramConnection(ctx context.Context, req common.OpenConnectionRequest, preset common.Preset) {
	localDialer := &net.Dialer{Timeout: 10 * time.Second}
	localConn, err := localDialer.DialContext(ctx, "udp", preset.LocalAddr)
	if err != nil {
		_ = s.sendConnectionError(req, fmt.Sprintf("dial local %s failed: %v", preset.LocalAddr, err))
		return
	}

	dataConn, err := s.openAttachConnection(ctx, req, common.NetworkUDP)
	if err != nil {
		_ = localConn.Close()
		_ = s.sendConnectionError(req, fmt.Sprintf("dial UDP data channel failed: %v", err))
		return
	}

	log.Printf("accepted %s session for preset=%s from %s", strings.ToUpper(common.NetworkUDP), req.PresetName, req.RemoteAddr)
	common.ProxyDatagrams(localConn, dataConn, nil, nil)
}

func (s *session) openAttachConnection(ctx context.Context, req common.OpenConnectionRequest, protocol string) (net.Conn, error) {
	serverDialer := &net.Dialer{Timeout: 10 * time.Second}
	dataConn, err := serverDialer.DialContext(ctx, "tcp", s.cfg.ServerAddr)
	if err != nil {
		return nil, err
	}

	attach := common.AttachRequest{
		Type:     common.MessageTypeAttach,
		Token:    s.cfg.Token,
		TunnelID: req.TunnelID,
		ConnID:   req.ConnID,
		NodeName: s.cfg.NodeName,
		Protocol: protocol,
	}
	if err := common.WriteMessage(dataConn, attach); err != nil {
		_ = dataConn.Close()
		return nil, fmt.Errorf("send attach failed: %w", err)
	}

	return dataConn, nil
}

func (s *session) sendConnectionError(req common.OpenConnectionRequest, message string) error {
	return s.sendControl(common.ConnectionError{
		Type:     common.MessageTypeConnectionError,
		TunnelID: req.TunnelID,
		ConnID:   req.ConnID,
		Error:    message,
	})
}

func (s *session) sendControl(message any) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	if s.controlConn == nil {
		return fmt.Errorf("control connection is not available")
	}

	return common.WriteMessage(s.controlConn, message)
}

func (s *session) logAssignments(msg common.AssignmentsMessage) {
	if len(msg.Tunnels) == 0 {
		log.Printf("server has not assigned any public endpoints to node %q yet", s.cfg.NodeName)
		return
	}

	log.Printf("server published %d public endpoint(s) for node %q", len(msg.Tunnels), s.cfg.NodeName)
	for _, tunnel := range msg.Tunnels {
		line := fmt.Sprintf("preset=%s protocol=%s local=%s public=%s", strings.ToUpper(tunnel.PresetName), strings.ToUpper(common.NormalizeProtocol(tunnel.Protocol)), tunnel.LocalAddr, tunnel.PublicTarget)
		if strings.TrimSpace(tunnel.CommandHint) != "" {
			line += " hint=\"" + tunnel.CommandHint + "\""
		}
		log.Print(line)
	}
}
