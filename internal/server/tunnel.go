package server

import (
	"bufio"
	"errors"
	"log"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"tunnel/internal/common"
)

type Tunnel struct {
	server     *Server
	id         string
	nodeName   string
	presetName string
	protocol   string
	publicPort int
	publicAddr string

	tcpListener net.Listener
	udpConn     net.PacketConn
	createdAt   time.Time

	pendingMu sync.Mutex
	pending   map[string]*pendingConn

	udpMu       sync.Mutex
	udpSessions map[string]*udpSession

	totalConnections  atomic.Uint64
	activeConnections atomic.Int64
	ingressBytes      atomic.Uint64
	egressBytes       atomic.Uint64
	lastActivityUnix  atomic.Int64

	lastErrorMu sync.RWMutex
	lastError   string
	lastErrorAt time.Time

	closed    chan struct{}
	closeOnce sync.Once
}

type pendingConn struct {
	conn  net.Conn
	timer *time.Timer
}

type udpSession struct {
	tunnel       *Tunnel
	connID       string
	remoteKey    string
	remoteAddr   net.Addr
	inbound      chan []byte
	stream       net.Conn
	attached     bool
	attachTimer  *time.Timer
	idleTimer    *time.Timer
	timerMu      sync.Mutex
	closed       chan struct{}
	closeOnce    sync.Once
	lastSeenUnix atomic.Int64
}

func (t *Tunnel) start() {
	switch t.protocol {
	case common.NetworkUDP:
		go t.servePacketLoop()
	default:
		go t.acceptLoop()
	}
}

func (t *Tunnel) acceptLoop() {
	log.Printf("public %s tunnel %q listening on %s", strings.ToUpper(t.protocol), t.id, t.publicAddr)
	for {
		conn, err := t.tcpListener.Accept()
		if err != nil {
			select {
			case <-t.closed:
				return
			default:
			}
			log.Printf("public accept failed on tunnel %q: %v", t.id, err)
			t.Close("public listener failed")
			return
		}

		go t.handlePublicConn(conn)
	}
}

func (t *Tunnel) servePacketLoop() {
	log.Printf("public %s tunnel %q listening on %s", strings.ToUpper(t.protocol), t.id, t.publicAddr)
	buffer := make([]byte, common.MaxDatagramSize)
	for {
		n, remoteAddr, err := t.udpConn.ReadFrom(buffer)
		if err != nil {
			select {
			case <-t.closed:
				return
			default:
			}
			log.Printf("public UDP read failed on tunnel %q: %v", t.id, err)
			t.Close("public UDP listener failed")
			return
		}

		payload := append([]byte(nil), buffer[:n]...)
		go t.handlePublicDatagram(remoteAddr, payload)
	}
}

func (t *Tunnel) handlePublicConn(publicConn net.Conn) {
	node := t.server.getNode(t.nodeName)
	if node == nil {
		_ = publicConn.Close()
		return
	}

	connID := common.NewConnID()
	timeout := time.Duration(t.server.cfg.AttachTimeoutSec) * time.Second

	pending := &pendingConn{
		conn: publicConn,
	}
	pending.timer = time.AfterFunc(timeout, func() {
		t.failPending(connID, "client did not attach data channel in time")
	})

	t.pendingMu.Lock()
	select {
	case <-t.closed:
		t.pendingMu.Unlock()
		_ = publicConn.Close()
		return
	default:
		t.pending[connID] = pending
	}
	t.pendingMu.Unlock()

	t.totalConnections.Add(1)
	t.touch()

	msg := common.OpenConnectionRequest{
		Type:       common.MessageTypeOpenConnection,
		TunnelID:   t.id,
		PresetName: t.presetName,
		ConnID:     connID,
		RemoteAddr: publicConn.RemoteAddr().String(),
		Protocol:   common.NetworkTCP,
	}

	if err := node.sendControl(msg); err != nil {
		log.Printf("notify node %q failed for tunnel %q: %v", t.nodeName, t.id, err)
		t.failPending(connID, "node is offline")
		return
	}
}

func (t *Tunnel) handlePublicDatagram(remoteAddr net.Addr, payload []byte) {
	node := t.server.getNode(t.nodeName)
	if node == nil {
		return
	}

	session, created := t.getOrCreateUDPSession(remoteAddr)
	if created {
		msg := common.OpenConnectionRequest{
			Type:       common.MessageTypeOpenConnection,
			TunnelID:   t.id,
			PresetName: t.presetName,
			ConnID:     session.connID,
			RemoteAddr: remoteAddr.String(),
			Protocol:   common.NetworkUDP,
		}
		if err := node.sendControl(msg); err != nil {
			log.Printf("notify node %q failed for UDP tunnel %q: %v", t.nodeName, t.id, err)
			session.close("node is offline")
			return
		}
	}

	session.touch()
	if !session.enqueue(payload) {
		t.setLastError("UDP session buffer is full")
	}
}

func (t *Tunnel) getOrCreateUDPSession(remoteAddr net.Addr) (*udpSession, bool) {
	key := remoteAddr.String()

	t.udpMu.Lock()
	defer t.udpMu.Unlock()

	if session, ok := t.udpSessions[key]; ok {
		return session, false
	}

	session := &udpSession{
		tunnel:     t,
		connID:     common.NewConnID(),
		remoteKey:  key,
		remoteAddr: remoteAddr,
		inbound:    make(chan []byte, 64),
		closed:     make(chan struct{}),
	}
	session.touch()
	session.startAttachTimeout()

	t.udpSessions[key] = session
	t.totalConnections.Add(1)
	t.activeConnections.Add(1)
	t.touch()
	return session, true
}

func (t *Tunnel) takePending(connID string) (net.Conn, bool) {
	t.pendingMu.Lock()
	defer t.pendingMu.Unlock()

	item, ok := t.pending[connID]
	if !ok {
		return nil, false
	}

	delete(t.pending, connID)
	if item.timer != nil {
		item.timer.Stop()
	}
	return item.conn, true
}

func (t *Tunnel) findUDPSession(connID string) *udpSession {
	t.udpMu.Lock()
	defer t.udpMu.Unlock()

	for _, session := range t.udpSessions {
		if session.connID == connID {
			return session
		}
	}
	return nil
}

func (t *Tunnel) failPending(connID, reason string) {
	conn, ok := t.takePending(connID)
	if !ok {
		return
	}

	t.setLastError(reason)
	log.Printf("closing pending conn_id=%s on tunnel %q: %s", connID, t.id, reason)
	_ = conn.Close()
}

func (t *Tunnel) beginTransfer() {
	t.activeConnections.Add(1)
	t.clearLastError()
	t.touch()
}

func (t *Tunnel) endTransfer() {
	current := t.activeConnections.Add(-1)
	if current < 0 {
		t.activeConnections.Store(0)
	}
	t.touch()
}

func (t *Tunnel) addIngressBytes(n uint64) {
	t.ingressBytes.Add(n)
	t.touch()
}

func (t *Tunnel) addEgressBytes(n uint64) {
	t.egressBytes.Add(n)
	t.touch()
}

func (t *Tunnel) setLastError(message string) {
	t.lastErrorMu.Lock()
	t.lastError = strings.TrimSpace(message)
	t.lastErrorAt = time.Now()
	t.lastErrorMu.Unlock()
	t.touch()
}

func (t *Tunnel) clearLastError() {
	t.lastErrorMu.Lock()
	t.lastError = ""
	t.lastErrorAt = time.Time{}
	t.lastErrorMu.Unlock()
}

func (t *Tunnel) touch() {
	t.lastActivityUnix.Store(time.Now().UnixNano())
}

func (t *Tunnel) pendingCount() int {
	t.pendingMu.Lock()
	count := len(t.pending)
	t.pendingMu.Unlock()

	t.udpMu.Lock()
	for _, session := range t.udpSessions {
		if !session.isAttached() {
			count++
		}
	}
	t.udpMu.Unlock()
	return count
}

func (t *Tunnel) lastActivity() time.Time {
	unixNano := t.lastActivityUnix.Load()
	if unixNano == 0 {
		return time.Time{}
	}
	return time.Unix(0, unixNano)
}

func (t *Tunnel) lastErrorSnapshot() (string, time.Time) {
	t.lastErrorMu.RLock()
	defer t.lastErrorMu.RUnlock()
	return t.lastError, t.lastErrorAt
}

func (t *Tunnel) applyPersistedStats(stats persistedTunnel) {
	if stats.CreatedAt > 0 {
		t.createdAt = time.Unix(stats.CreatedAt, 0)
	}
	t.totalConnections.Store(stats.TotalConnections)
	t.ingressBytes.Store(stats.IngressBytes)
	t.egressBytes.Store(stats.EgressBytes)
	t.lastActivityUnix.Store(stats.LastActivityUnix)
	if stats.LastError != "" {
		t.lastErrorMu.Lock()
		t.lastError = stats.LastError
		if stats.LastErrorAt > 0 {
			t.lastErrorAt = time.Unix(stats.LastErrorAt, 0)
		}
		t.lastErrorMu.Unlock()
	}
}

func (t *Tunnel) Close(reason string) {
	t.closeOnce.Do(func() {
		log.Printf("closing tunnel %q: %s", t.id, reason)
		close(t.closed)

		if t.tcpListener != nil {
			_ = t.tcpListener.Close()
		}
		if t.udpConn != nil {
			_ = t.udpConn.Close()
		}

		t.pendingMu.Lock()
		pending := t.pending
		t.pending = make(map[string]*pendingConn)
		t.pendingMu.Unlock()

		for _, item := range pending {
			if item.timer != nil {
				item.timer.Stop()
			}
			_ = item.conn.Close()
		}

		t.udpMu.Lock()
		sessions := make([]*udpSession, 0, len(t.udpSessions))
		for _, session := range t.udpSessions {
			sessions = append(sessions, session)
		}
		t.udpSessions = make(map[string]*udpSession)
		t.udpMu.Unlock()

		for _, session := range sessions {
			session.close("")
		}

		t.server.unregisterTunnel(t)
	})
}

func (s *udpSession) startAttachTimeout() {
	timeout := time.Duration(s.tunnel.server.cfg.AttachTimeoutSec) * time.Second
	s.timerMu.Lock()
	s.attachTimer = time.AfterFunc(timeout, func() {
		s.close("client did not attach UDP data channel in time")
	})
	s.timerMu.Unlock()
}

func (s *udpSession) startIdleTimeout() {
	timeout := time.Duration(s.tunnel.server.cfg.UDPSessionTimeoutSec) * time.Second
	s.timerMu.Lock()
	if s.idleTimer != nil {
		s.idleTimer.Stop()
	}
	s.idleTimer = time.AfterFunc(timeout, func() {
		s.close("UDP session idle timeout")
	})
	s.timerMu.Unlock()
}

func (s *udpSession) touch() {
	s.lastSeenUnix.Store(time.Now().UnixNano())

	s.timerMu.Lock()
	if s.attached && s.idleTimer != nil {
		s.idleTimer.Reset(time.Duration(s.tunnel.server.cfg.UDPSessionTimeoutSec) * time.Second)
	}
	s.timerMu.Unlock()
	s.tunnel.touch()
}

func (s *udpSession) isAttached() bool {
	s.timerMu.Lock()
	defer s.timerMu.Unlock()
	return s.attached
}

func (s *udpSession) enqueue(payload []byte) bool {
	select {
	case s.inbound <- payload:
		return true
	case <-s.closed:
		return false
	default:
		return false
	}
}

func (s *udpSession) attach(stream net.Conn) {
	s.timerMu.Lock()
	if s.attached {
		s.timerMu.Unlock()
		_ = stream.Close()
		return
	}
	s.stream = stream
	s.attached = true
	if s.attachTimer != nil {
		s.attachTimer.Stop()
		s.attachTimer = nil
	}
	s.timerMu.Unlock()

	s.tunnel.clearLastError()
	s.touch()
	s.startIdleTimeout()

	go s.pipePublicToStream()
	go s.pipeStreamToPublic()
}

func (s *udpSession) pipePublicToStream() {
	for {
		select {
		case <-s.closed:
			return
		case payload := <-s.inbound:
			if len(payload) == 0 {
				continue
			}
			if err := common.WriteFrame(s.stream, payload); err != nil {
				s.close("UDP upstream write failed")
				return
			}
			s.tunnel.addIngressBytes(uint64(len(payload)))
			s.touch()
		}
	}
}

func (s *udpSession) pipeStreamToPublic() {
	reader := bufio.NewReader(s.stream)
	for {
		payload, err := common.ReadFrame(reader)
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				s.close("UDP downstream closed")
			} else {
				s.close("")
			}
			return
		}
		if len(payload) == 0 {
			continue
		}

		n, writeErr := s.tunnel.udpConn.WriteTo(payload, s.remoteAddr)
		if writeErr != nil {
			s.close("public UDP write failed")
			return
		}
		if n > 0 {
			s.tunnel.addEgressBytes(uint64(n))
		}
		s.touch()
	}
}

func (s *udpSession) close(reason string) {
	s.closeOnce.Do(func() {
		if strings.TrimSpace(reason) != "" {
			log.Printf("closing UDP session %s on tunnel %q: %s", s.connID, s.tunnel.id, reason)
			s.tunnel.setLastError(reason)
		}

		close(s.closed)

		s.timerMu.Lock()
		if s.attachTimer != nil {
			s.attachTimer.Stop()
		}
		if s.idleTimer != nil {
			s.idleTimer.Stop()
		}
		stream := s.stream
		s.timerMu.Unlock()

		if stream != nil {
			_ = stream.Close()
		}

		s.tunnel.udpMu.Lock()
		delete(s.tunnel.udpSessions, s.remoteKey)
		s.tunnel.udpMu.Unlock()

		current := s.tunnel.activeConnections.Add(-1)
		if current < 0 {
			s.tunnel.activeConnections.Store(0)
		}
		s.tunnel.touch()
	})
}
