package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"tunnel/internal/common"
)

type Server struct {
	cfg Config

	controlListener net.Listener
	httpServer      *http.Server

	createTunnelMu sync.Mutex
	persistMu      sync.Mutex

	mu      sync.RWMutex
	nodes   map[string]*Node
	tunnels map[string]*Tunnel
	ports   map[int]*Tunnel

	sessionMu sync.Mutex
	sessions  map[string]*webSession
}

type Node struct {
	name               string
	hostname           string
	platform           string
	remote             string
	accessToken        string
	credentialIssuedAt time.Time

	presets map[string]common.Preset

	connected   bool
	connectedAt time.Time
	lastSeen    time.Time

	lastProvisionError   string
	lastProvisionErrorAt time.Time

	controlConn net.Conn

	mu             sync.RWMutex
	controlWriteMu sync.Mutex
}

func Run(ctx context.Context, cfg Config) error {
	srv := &Server{
		cfg:      cfg,
		nodes:    make(map[string]*Node),
		tunnels:  make(map[string]*Tunnel),
		ports:    make(map[int]*Tunnel),
		sessions: make(map[string]*webSession),
	}
	return srv.Run(ctx)
}

func (s *Server) Run(ctx context.Context) error {
	controlListener, err := net.Listen("tcp", s.cfg.ControlListenAddr)
	if err != nil {
		return err
	}
	s.controlListener = controlListener

	if err := s.restoreState(); err != nil {
		_ = controlListener.Close()
		return err
	}

	mux := http.NewServeMux()
	s.registerRoutes(mux)

	httpServer := &http.Server{
		Addr:    s.cfg.HTTPListenAddr,
		Handler: mux,
	}
	s.httpServer = httpServer

	s.startStateSaver(ctx)

	errCh := make(chan error, 1)
	go func() {
		log.Printf("web console listening on %s", s.cfg.HTTPListenAddr)
		err := httpServer.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			_ = controlListener.Close()
		}
	}()

	log.Printf("%s control server listening on %s", common.Version, s.cfg.ControlListenAddr)
	s.printStartupDashboard()

	go func() {
		<-ctx.Done()
		s.shutdown()
	}()

	for {
		conn, err := controlListener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}

			select {
			case httpErr := <-errCh:
				return httpErr
			default:
			}

			log.Printf("accept control connection failed: %v", err)
			continue
		}

		go s.handleControlSession(conn)
	}
}

func (s *Server) startStateSaver(ctx context.Context) {
	interval := time.Duration(s.cfg.StateSaveIntervalSec) * time.Second
	if interval <= 0 {
		return
	}

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.saveStateQuiet("periodic snapshot")
			}
		}
	}()
}

func (s *Server) shutdown() {
	s.saveStateQuiet("shutdown snapshot")

	if s.controlListener != nil {
		_ = s.controlListener.Close()
	}

	if s.httpServer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.httpServer.Shutdown(ctx)
	}

	s.mu.RLock()
	nodes := make([]*Node, 0, len(s.nodes))
	for _, node := range s.nodes {
		nodes = append(nodes, node)
	}
	tunnels := make([]*Tunnel, 0, len(s.tunnels))
	for _, tunnel := range s.tunnels {
		tunnels = append(tunnels, tunnel)
	}
	s.mu.RUnlock()

	for _, node := range nodes {
		node.closeControl()
	}
	for _, tunnel := range tunnels {
		tunnel.Close("server shutdown")
	}
}

func (s *Server) handleControlSession(conn net.Conn) {
	reader := bufio.NewReader(conn)
	_ = conn.SetDeadline(time.Now().Add(time.Duration(s.cfg.HandshakeTimeoutSec) * time.Second))

	line, err := common.ReadMessageLine(reader)
	if err != nil {
		log.Printf("handshake read failed from %s: %v", conn.RemoteAddr(), err)
		_ = conn.Close()
		return
	}

	var envelope common.Envelope
	if err := json.Unmarshal(line, &envelope); err != nil {
		log.Printf("handshake decode failed from %s: %v", conn.RemoteAddr(), err)
		_ = conn.Close()
		return
	}

	switch envelope.Type {
	case common.MessageTypeRegisterNode:
		var req common.RegisterNodeRequest
		if err := json.Unmarshal(line, &req); err != nil {
			log.Printf("register node decode failed from %s: %v", conn.RemoteAddr(), err)
			_ = conn.Close()
			return
		}
		s.handleRegisterNode(conn, reader, req)
	case common.MessageTypeAttach:
		var req common.AttachRequest
		if err := json.Unmarshal(line, &req); err != nil {
			log.Printf("attach decode failed from %s: %v", conn.RemoteAddr(), err)
			_ = conn.Close()
			return
		}
		s.handleAttach(conn, req)
	default:
		log.Printf("unknown control handshake %q from %s", envelope.Type, conn.RemoteAddr())
		_ = conn.Close()
	}
}

func (s *Server) handleRegisterNode(conn net.Conn, reader *bufio.Reader, req common.RegisterNodeRequest) {
	resp := common.RegisterNodeResponse{
		Type: common.MessageTypeRegisterNodeResponse,
	}

	node, oldConn, err := s.registerNode(conn, req)
	if err != nil {
		resp.Error = err.Error()
		if writeErr := common.WriteMessage(conn, resp); writeErr != nil {
			log.Printf("send register_node error failed: %v", writeErr)
		}
		_ = conn.Close()
		return
	}

	resp.OK = true
	if err := common.WriteMessage(conn, resp); err != nil {
		log.Printf("send register_node response failed for %q: %v", req.NodeName, err)
		_ = conn.Close()
		node.markDisconnected(conn)
		return
	}

	if oldConn != nil && oldConn != conn {
		_ = oldConn.Close()
	}

	s.saveStateQuiet("node registration updated")
	s.syncNodeTunnels(node.name)

	_ = conn.SetDeadline(time.Time{})
	s.nodeControlLoop(node, conn, reader)
}

func (s *Server) registerNode(conn net.Conn, req common.RegisterNodeRequest) (*Node, net.Conn, error) {
	nodeName := strings.TrimSpace(req.NodeName)
	if nodeName == "" {
		return nil, nil, fmt.Errorf("node_name is required")
	}

	s.mu.RLock()
	node := s.nodes[nodeName]
	s.mu.RUnlock()
	if node == nil {
		return nil, nil, fmt.Errorf("node %q does not have an issued credential yet", nodeName)
	}

	accessToken := strings.TrimSpace(req.Token)
	if accessToken == "" || !node.matchesAccessToken(accessToken) {
		return nil, nil, fmt.Errorf("invalid credential for node %q", nodeName)
	}

	presets := make(map[string]common.Preset)
	for _, preset := range req.Presets {
		name := strings.TrimSpace(preset.Name)
		localAddr := strings.TrimSpace(preset.LocalAddr)
		protocol := common.NormalizeProtocol(preset.Protocol)
		if name == "" || localAddr == "" || !common.IsSupportedProtocol(protocol) {
			continue
		}
		preset.Name = name
		preset.LocalAddr = localAddr
		preset.Protocol = protocol
		presets[name] = preset
	}
	if len(presets) == 0 {
		return nil, nil, fmt.Errorf("at least one valid preset is required")
	}

	oldConn := node.updateRegistration(conn, req.Hostname, req.Platform, conn.RemoteAddr().String(), presets)
	log.Printf("node %q connected from %s with %d preset(s)", nodeName, conn.RemoteAddr(), len(presets))
	return node, oldConn, nil
}

func (s *Server) nodeControlLoop(node *Node, conn net.Conn, reader *bufio.Reader) {
	for {
		line, err := common.ReadMessageLine(reader)
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				log.Printf("control loop for node %q closed: %v", node.name, err)
			}
			node.markDisconnected(conn)
			s.saveStateQuiet("node disconnected")
			_ = conn.Close()
			return
		}

		node.touch()

		var envelope common.Envelope
		if err := json.Unmarshal(line, &envelope); err != nil {
			log.Printf("invalid control message from node %q: %v", node.name, err)
			continue
		}

		switch envelope.Type {
		case common.MessageTypeConnectionError:
			var msg common.ConnectionError
			if err := json.Unmarshal(line, &msg); err != nil {
				log.Printf("decode connection_error failed for node %q: %v", node.name, err)
				continue
			}
			tunnel := s.getTunnel(msg.TunnelID)
			if tunnel != nil {
				tunnel.failPending(msg.ConnID, msg.Error)
			}
		default:
			log.Printf("unknown control message %q from node %q", envelope.Type, node.name)
		}
	}
}

func (s *Server) handleAttach(conn net.Conn, req common.AttachRequest) {
	tunnel := s.getTunnel(req.TunnelID)
	if tunnel == nil {
		log.Printf("reject attach for unknown tunnel %q", req.TunnelID)
		_ = conn.Close()
		return
	}

	node := s.getNode(tunnel.nodeName)
	if node == nil {
		log.Printf("reject attach for tunnel %q: node %q is missing", req.TunnelID, tunnel.nodeName)
		_ = conn.Close()
		return
	}

	if strings.TrimSpace(req.NodeName) != "" && !strings.EqualFold(strings.TrimSpace(req.NodeName), node.name) {
		log.Printf("reject attach for tunnel %q: node mismatch %q", req.TunnelID, req.NodeName)
		_ = conn.Close()
		return
	}
	if !node.matchesAccessToken(req.Token) {
		log.Printf("reject attach from %s: invalid credential for node %q", conn.RemoteAddr(), node.name)
		_ = conn.Close()
		return
	}

	switch tunnel.protocol {
	case common.NetworkUDP:
		session := tunnel.findUDPSession(req.ConnID)
		if session == nil {
			log.Printf("reject UDP attach for missing conn_id=%s on tunnel %q", req.ConnID, req.TunnelID)
			_ = conn.Close()
			return
		}
		_ = conn.SetDeadline(time.Time{})
		session.attach(conn)
	case common.NetworkTCP:
		publicConn, ok := tunnel.takePending(req.ConnID)
		if !ok {
			log.Printf("reject attach for missing conn_id=%s on tunnel %q", req.ConnID, req.TunnelID)
			_ = conn.Close()
			return
		}

		_ = conn.SetDeadline(time.Time{})
		_ = publicConn.SetDeadline(time.Time{})
		tunnel.beginTransfer()
		defer tunnel.endTransfer()

		log.Printf("attached conn_id=%s on tunnel %q", req.ConnID, req.TunnelID)
		common.ProxyWithAccounting(
			publicConn,
			conn,
			tunnel.addIngressBytes,
			tunnel.addEgressBytes,
		)
	default:
		log.Printf("reject attach for tunnel %q: unsupported protocol %q", req.TunnelID, tunnel.protocol)
		_ = conn.Close()
	}
}

func (s *Server) createTunnel(nodeName, presetName string, requestedPort int) (*Tunnel, error) {
	return s.createTunnelInternal(nodeName, presetName, requestedPort, "")
}

func (s *Server) createTunnelInternal(nodeName, presetName string, requestedPort int, allowExistingTunnelID string) (*Tunnel, error) {
	s.createTunnelMu.Lock()
	defer s.createTunnelMu.Unlock()

	node := s.getNode(nodeName)
	if node == nil {
		return nil, fmt.Errorf("node %q not found", nodeName)
	}
	preset, ok := node.preset(presetName)
	if !ok {
		return nil, fmt.Errorf("node %q does not expose preset %q", nodeName, presetName)
	}

	s.mu.RLock()
	for _, tunnel := range s.tunnels {
		if tunnel.nodeName == nodeName && tunnel.presetName == presetName && tunnel.id != allowExistingTunnelID {
			s.mu.RUnlock()
			return nil, fmt.Errorf("preset %q on node %q is already mapped on port %d", presetName, nodeName, tunnel.publicPort)
		}
	}
	s.mu.RUnlock()

	candidates, err := s.portCandidates(requestedPort)
	if err != nil {
		return nil, err
	}

	protocol := common.NormalizeProtocol(preset.Protocol)
	var (
		tcpListener net.Listener
		udpConn     net.PacketConn
		publicAddr  string
		publicPort  int
	)
	for _, port := range candidates {
		if requestedPort == 0 && s.isPortTracked(port) {
			continue
		}

		publicAddr = fmt.Sprintf("%s:%d", s.cfg.PublicBindAddr, port)
		switch protocol {
		case common.NetworkUDP:
			udpConn, err = net.ListenPacket("udp", publicAddr)
		default:
			tcpListener, err = net.Listen("tcp", publicAddr)
		}
		if err != nil {
			if requestedPort != 0 {
				return nil, fmt.Errorf("listen on %s (%s) failed: %w", publicAddr, protocol, err)
			}
			continue
		}

		publicPort = port
		break
	}

	if publicPort == 0 {
		return nil, fmt.Errorf("no free public port available in range %d-%d", s.cfg.AutoPortStart, s.cfg.AutoPortEnd)
	}

	tunnel := &Tunnel{
		server:      s,
		id:          common.NewTunnelID(),
		nodeName:    nodeName,
		presetName:  presetName,
		protocol:    protocol,
		publicPort:  publicPort,
		publicAddr:  publicAddr,
		tcpListener: tcpListener,
		udpConn:     udpConn,
		createdAt:   time.Now(),
		pending:     make(map[string]*pendingConn),
		udpSessions: make(map[string]*udpSession),
		closed:      make(chan struct{}),
	}
	tunnel.touch()

	s.mu.Lock()
	s.tunnels[tunnel.id] = tunnel
	s.ports[publicPort] = tunnel
	s.mu.Unlock()

	tunnel.start()
	s.saveStateQuiet("tunnel created")
	log.Printf("created tunnel %q for node=%s preset=%s protocol=%s public_port=%d", tunnel.id, nodeName, presetName, protocol, publicPort)
	return tunnel, nil
}

func (s *Server) reassignTunnel(id string) (*Tunnel, error) {
	current := s.getTunnel(id)
	if current == nil {
		return nil, fmt.Errorf("tunnel %q not found", id)
	}

	replacement, err := s.createTunnelInternal(current.nodeName, current.presetName, 0, current.id)
	if err != nil {
		return nil, err
	}

	current.Close("reassigned from web console")
	s.pushAssignments(current.nodeName)
	s.saveStateQuiet("tunnel reassigned")
	return replacement, nil
}

func (s *Server) syncNodeTunnels(nodeName string) {
	node := s.getNode(nodeName)
	if node == nil {
		return
	}

	desired := node.copyPresets()
	existing := s.listTunnelsForNode(nodeName)
	existingByPreset := make(map[string]*Tunnel, len(existing))
	for _, tunnel := range existing {
		existingByPreset[tunnel.presetName] = tunnel
	}

	for _, tunnel := range existing {
		desiredPreset, ok := desired[tunnel.presetName]
		if !ok {
			tunnel.Close("preset removed from node registration")
			continue
		}
		if common.NormalizeProtocol(desiredPreset.Protocol) != tunnel.protocol {
			tunnel.Close("preset protocol changed")
		}
	}

	missing := make([]string, 0)
	for presetName := range desired {
		tunnel, ok := existingByPreset[presetName]
		if !ok || tunnel == nil || tunnel.protocol != common.NormalizeProtocol(desired[presetName].Protocol) {
			missing = append(missing, presetName)
		}
	}
	sort.Strings(missing)

	var provisionErrors []string
	for _, presetName := range missing {
		if _, err := s.createTunnel(nodeName, presetName, 0); err != nil {
			provisionErrors = append(provisionErrors, fmt.Sprintf("%s: %v", presetName, err))
			log.Printf("auto provisioning failed for node=%s preset=%s: %v", nodeName, presetName, err)
		}
	}

	if len(provisionErrors) > 0 {
		node.setProvisionError(strings.Join(provisionErrors, "; "))
	} else {
		node.clearProvisionError()
	}

	s.pushAssignments(nodeName)
	s.saveStateQuiet("node tunnel sync")
}

func (s *Server) pushAssignments(nodeName string) {
	node := s.getNode(nodeName)
	if node == nil {
		return
	}

	publicHost := s.resolvedPublicHost()
	assignments := make([]common.AssignedTunnel, 0)
	for _, tunnel := range s.listTunnelsForNode(nodeName) {
		presetProtocol := node.presetProtocol(tunnel.presetName)
		assignments = append(assignments, common.AssignedTunnel{
			TunnelID:     tunnel.id,
			PresetName:   tunnel.presetName,
			LocalAddr:    node.presetLocalAddr(tunnel.presetName),
			PublicPort:   tunnel.publicPort,
			PublicTarget: fmt.Sprintf("%s:%d", publicHost, tunnel.publicPort),
			CommandHint:  buildCommandHint(publicHost, presetProtocol, tunnel.presetName, tunnel.publicPort),
			Protocol:     presetProtocol,
		})
	}

	msg := common.AssignmentsMessage{
		Type:       common.MessageTypeAssignments,
		PublicHost: publicHost,
		UpdatedAt:  time.Now().Unix(),
		Tunnels:    assignments,
	}

	if err := node.sendControl(msg); err != nil {
		log.Printf("send assignments to node %q failed: %v", nodeName, err)
	}
}

func (s *Server) portCandidates(requestedPort int) ([]int, error) {
	if requestedPort != 0 {
		return []int{requestedPort}, nil
	}

	s.mu.RLock()
	ports := make([]int, 0, (s.cfg.AutoPortEnd-s.cfg.AutoPortStart)+1)
	for port := s.cfg.AutoPortStart; port <= s.cfg.AutoPortEnd; port++ {
		if _, used := s.ports[port]; !used {
			ports = append(ports, port)
		}
	}
	s.mu.RUnlock()

	if len(ports) == 0 {
		return nil, fmt.Errorf("no free public port available in range %d-%d", s.cfg.AutoPortStart, s.cfg.AutoPortEnd)
	}

	randomizer := rand.New(rand.NewSource(time.Now().UnixNano()))
	randomizer.Shuffle(len(ports), func(i, j int) {
		ports[i], ports[j] = ports[j], ports[i]
	})
	return ports, nil
}

func (s *Server) isPortTracked(port int) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, exists := s.ports[port]
	return exists
}

func (s *Server) getNode(name string) *Node {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.nodes[name]
}

func (s *Server) getTunnel(id string) *Tunnel {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.tunnels[id]
}

func (s *Server) listNodes() []*Node {
	s.mu.RLock()
	nodes := make([]*Node, 0, len(s.nodes))
	for _, node := range s.nodes {
		nodes = append(nodes, node)
	}
	s.mu.RUnlock()

	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].name < nodes[j].name
	})
	return nodes
}

func (s *Server) listTunnels() []*Tunnel {
	s.mu.RLock()
	tunnels := make([]*Tunnel, 0, len(s.tunnels))
	for _, tunnel := range s.tunnels {
		tunnels = append(tunnels, tunnel)
	}
	s.mu.RUnlock()

	sort.Slice(tunnels, func(i, j int) bool {
		if tunnels[i].publicPort == tunnels[j].publicPort {
			if tunnels[i].protocol == tunnels[j].protocol {
				return tunnels[i].id < tunnels[j].id
			}
			return tunnels[i].protocol < tunnels[j].protocol
		}
		return tunnels[i].publicPort < tunnels[j].publicPort
	})
	return tunnels
}

func (s *Server) listTunnelsForNode(nodeName string) []*Tunnel {
	tunnels := make([]*Tunnel, 0)
	for _, tunnel := range s.listTunnels() {
		if tunnel.nodeName == nodeName {
			tunnels = append(tunnels, tunnel)
		}
	}
	return tunnels
}

func (s *Server) unregisterTunnel(tunnel *Tunnel) {
	s.mu.Lock()
	defer s.mu.Unlock()

	current, ok := s.tunnels[tunnel.id]
	if ok && current == tunnel {
		delete(s.tunnels, tunnel.id)
	}
	if current, ok := s.ports[tunnel.publicPort]; ok && current == tunnel {
		delete(s.ports, tunnel.publicPort)
	}
}

func (s *Server) resolvedPublicHost() string {
	publicHost := strings.TrimSpace(s.cfg.PublicHost)
	if publicHost != "" {
		if host, _, err := net.SplitHostPort(publicHost); err == nil {
			publicHost = host
		}
		return strings.Trim(publicHost, "[]")
	}

	host, _, err := splitHostPortLoose(s.cfg.ControlListenAddr)
	if err == nil {
		host = normalizePublicHost(host)
		if host != "" {
			return host
		}
	}

	return "127.0.0.1"
}

func (s *Server) nodeConnectToken(node *Node) string {
	serverAddr, err := s.publicControlAddress()
	if err != nil || node == nil {
		return ""
	}

	node.mu.RLock()
	payload := common.BootstrapTokenPayload{
		ServerAddr:  serverAddr,
		NodeName:    node.name,
		AccessToken: node.accessToken,
	}
	if !node.credentialIssuedAt.IsZero() {
		payload.IssuedAt = node.credentialIssuedAt.Unix()
	}
	node.mu.RUnlock()

	if strings.TrimSpace(payload.AccessToken) == "" || strings.TrimSpace(payload.NodeName) == "" {
		return ""
	}

	token, err := common.EncodeBootstrapToken(payload)
	if err != nil {
		return ""
	}
	return token
}

func (s *Server) issueNodeCredential(nodeName string) (*Node, string, bool, error) {
	nodeName = strings.TrimSpace(nodeName)
	if nodeName == "" {
		return nil, "", false, fmt.Errorf("node_name is required")
	}
	if _, err := s.publicControlAddress(); err != nil {
		return nil, "", false, fmt.Errorf("public_host is not ready for issuing bootstrap tokens: %w", err)
	}

	accessToken, err := generateSessionToken()
	if err != nil {
		return nil, "", false, fmt.Errorf("generate node credential: %w", err)
	}

	s.mu.Lock()
	node, created, oldConn := s.issueNodeCredentialLocked(nodeName, accessToken)
	s.mu.Unlock()

	if oldConn != nil {
		_ = oldConn.Close()
	}

	s.saveStateQuiet("node credential issued")
	return node, s.nodeConnectToken(node), created, nil
}

func (s *Server) deleteNode(nodeName string) (int, bool, error) {
	nodeName = strings.TrimSpace(nodeName)
	if nodeName == "" {
		return 0, false, fmt.Errorf("node_name is required")
	}

	s.mu.Lock()
	node := s.nodes[nodeName]
	if node == nil {
		s.mu.Unlock()
		return 0, false, fmt.Errorf("node %q not found", nodeName)
	}

	tunnels := make([]*Tunnel, 0)
	for _, tunnel := range s.tunnels {
		if tunnel.nodeName == nodeName {
			tunnels = append(tunnels, tunnel)
		}
	}
	delete(s.nodes, nodeName)
	s.mu.Unlock()

	node.mu.RLock()
	wasConnected := node.connected
	node.mu.RUnlock()

	node.closeControl()
	for _, tunnel := range tunnels {
		tunnel.Close("node deleted from web console")
	}

	s.saveStateQuiet("node deleted")
	return len(tunnels), wasConnected, nil
}

func (s *Server) issueNodeCredentialLocked(nodeName, accessToken string) (*Node, bool, net.Conn) {
	node := s.nodes[nodeName]
	created := false
	if node == nil {
		node = &Node{
			name:    nodeName,
			presets: make(map[string]common.Preset),
		}
		s.nodes[nodeName] = node
		created = true
	}

	oldConn := node.setCredential(accessToken, time.Now())
	return node, created, oldConn
}

func buildCommandHint(publicHost, protocol, presetName string, publicPort int) string {
	protocol = common.NormalizeProtocol(protocol)
	switch protocol {
	case common.NetworkUDP:
		return "udp://" + publicHost + ":" + fmt.Sprintf("%d", publicPort)
	default:
		switch strings.ToLower(strings.TrimSpace(presetName)) {
		case "ssh":
			return "ssh -p " + fmt.Sprintf("%d", publicPort) + " user@" + publicHost
		case "rdp":
			return "mstsc /v:" + publicHost + ":" + fmt.Sprintf("%d", publicPort)
		default:
			return publicHost + ":" + fmt.Sprintf("%d", publicPort)
		}
	}
}

func (n *Node) updateRegistration(conn net.Conn, hostname, platform, remote string, presets map[string]common.Preset) net.Conn {
	n.mu.Lock()
	defer n.mu.Unlock()

	now := time.Now()
	oldConn := n.controlConn
	n.hostname = hostname
	n.platform = platform
	n.remote = remote
	n.connected = true
	n.connectedAt = now
	n.lastSeen = now
	n.controlConn = conn
	n.presets = presets
	return oldConn
}

func (n *Node) setCredential(accessToken string, issuedAt time.Time) net.Conn {
	n.mu.Lock()
	defer n.mu.Unlock()

	oldConn := n.controlConn
	n.accessToken = strings.TrimSpace(accessToken)
	n.credentialIssuedAt = issuedAt
	if oldConn != nil {
		n.controlConn = nil
		n.connected = false
		n.lastSeen = time.Now()
	}
	return oldConn
}

func (n *Node) matchesAccessToken(accessToken string) bool {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return strings.TrimSpace(accessToken) != "" && strings.TrimSpace(accessToken) == n.accessToken
}

func (n *Node) markDisconnected(conn net.Conn) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.controlConn == conn {
		n.controlConn = nil
		n.connected = false
		n.lastSeen = time.Now()
	}
}

func (n *Node) touch() {
	n.mu.Lock()
	n.lastSeen = time.Now()
	n.mu.Unlock()
}

func (n *Node) closeControl() {
	n.mu.RLock()
	conn := n.controlConn
	n.mu.RUnlock()

	if conn != nil {
		_ = conn.Close()
	}
}

func (n *Node) hasPreset(name string) bool {
	n.mu.RLock()
	defer n.mu.RUnlock()
	_, ok := n.presets[name]
	return ok
}

func (n *Node) preset(name string) (common.Preset, bool) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	preset, ok := n.presets[name]
	return preset, ok
}

func (n *Node) copyPresets() map[string]common.Preset {
	n.mu.RLock()
	defer n.mu.RUnlock()

	out := make(map[string]common.Preset, len(n.presets))
	for name, preset := range n.presets {
		out[name] = preset
	}
	return out
}

func (n *Node) presetLocalAddr(name string) string {
	n.mu.RLock()
	defer n.mu.RUnlock()
	preset, ok := n.presets[name]
	if !ok {
		return ""
	}
	return preset.LocalAddr
}

func (n *Node) presetProtocol(name string) string {
	n.mu.RLock()
	defer n.mu.RUnlock()
	preset, ok := n.presets[name]
	if !ok {
		return common.NetworkTCP
	}
	return common.NormalizeProtocol(preset.Protocol)
}

func (n *Node) sendControl(message any) error {
	n.controlWriteMu.Lock()
	defer n.controlWriteMu.Unlock()

	n.mu.RLock()
	conn := n.controlConn
	connected := n.connected
	n.mu.RUnlock()

	if !connected || conn == nil {
		return fmt.Errorf("node %q is offline", n.name)
	}

	if err := common.WriteMessage(conn, message); err != nil {
		n.markDisconnected(conn)
		_ = conn.Close()
		return err
	}

	n.touch()
	return nil
}

func (n *Node) setProvisionError(message string) {
	n.mu.Lock()
	n.lastProvisionError = strings.TrimSpace(message)
	n.lastProvisionErrorAt = time.Now()
	n.mu.Unlock()
}

func (n *Node) clearProvisionError() {
	n.mu.Lock()
	n.lastProvisionError = ""
	n.lastProvisionErrorAt = time.Time{}
	n.mu.Unlock()
}
