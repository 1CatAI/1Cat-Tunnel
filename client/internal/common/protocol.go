package common

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"time"
)

const (
	MessageTypeRegisterNode         = "register_node"
	MessageTypeRegisterNodeResponse = "register_node_response"
	MessageTypeOpenConnection       = "open_connection"
	MessageTypeConnectionError      = "connection_error"
	MessageTypeAttach               = "attach"
	MessageTypeAssignments          = "assignments"
	MessageTypeBlockIP              = "block_ip"
	MessageTypeBlockIPResponse      = "block_ip_response"

	NetworkTCP = "tcp"
	NetworkUDP = "udp"

	DefaultHandshakeMaxMessageSize = 16 * 1024
	DefaultControlMaxMessageSize   = 64 * 1024
)

type Envelope struct {
	Type string `json:"type"`
}

type Preset struct {
	Name        string `json:"name"`
	LocalAddr   string `json:"local_addr"`
	Description string `json:"description,omitempty"`
	Protocol    string `json:"protocol,omitempty"`
}

type RegisterNodeRequest struct {
	Type          string   `json:"type"`
	Token         string   `json:"token"`
	NodeName      string   `json:"node_name"`
	Hostname      string   `json:"hostname"`
	Platform      string   `json:"platform"`
	ClientVersion string   `json:"client_version,omitempty"`
	Presets       []Preset `json:"presets"`
}

type RegisterNodeResponse struct {
	Type        string `json:"type"`
	OK          bool   `json:"ok"`
	Error       string `json:"error,omitempty"`
	AccessToken string `json:"access_token,omitempty"`
}

type OpenConnectionRequest struct {
	Type       string `json:"type"`
	TunnelID   string `json:"tunnel_id"`
	PresetName string `json:"preset_name"`
	ConnID     string `json:"conn_id"`
	RemoteAddr string `json:"remote_addr,omitempty"`
	Protocol   string `json:"protocol,omitempty"`
}

type ConnectionError struct {
	Type     string `json:"type"`
	TunnelID string `json:"tunnel_id"`
	ConnID   string `json:"conn_id"`
	Error    string `json:"error"`
}

type BlockIPRequest struct {
	Type          string `json:"type"`
	RequestID     string `json:"request_id"`
	AdminUsername string `json:"admin_username"`
	AdminPassword string `json:"admin_password"`
	IP            string `json:"ip"`
	Reason        string `json:"reason,omitempty"`
}

type BlockIPResponse struct {
	Type          string `json:"type"`
	RequestID     string `json:"request_id"`
	OK            bool   `json:"ok"`
	IP            string `json:"ip,omitempty"`
	AlreadyExists bool   `json:"already_exists,omitempty"`
	Error         string `json:"error,omitempty"`
}

type AttachRequest struct {
	Type     string `json:"type"`
	Token    string `json:"token"`
	TunnelID string `json:"tunnel_id"`
	ConnID   string `json:"conn_id"`
	NodeName string `json:"node_name,omitempty"`
	Protocol string `json:"protocol,omitempty"`
}

type AssignedTunnel struct {
	TunnelID     string `json:"tunnel_id"`
	PresetName   string `json:"preset_name"`
	LocalAddr    string `json:"local_addr"`
	PublicPort   int    `json:"public_port"`
	PublicTarget string `json:"public_target"`
	CommandHint  string `json:"command_hint,omitempty"`
	Protocol     string `json:"protocol,omitempty"`
}

type AssignmentsMessage struct {
	Type       string           `json:"type"`
	PublicHost string           `json:"public_host"`
	UpdatedAt  int64            `json:"updated_at"`
	Tunnels    []AssignedTunnel `json:"tunnels"`
}

func ReadMessageLine(reader *bufio.Reader) ([]byte, error) {
	return ReadMessageLineLimit(reader, DefaultControlMaxMessageSize)
}

func ReadMessageLineLimit(reader *bufio.Reader, maxBytes int) ([]byte, error) {
	if maxBytes <= 0 {
		return nil, fmt.Errorf("message size limit must be greater than zero")
	}

	line := make([]byte, 0, min(maxBytes, 4096))
	for {
		fragment, err := reader.ReadSlice('\n')
		if len(line)+len(fragment) > maxBytes {
			return nil, fmt.Errorf("control message exceeds %d bytes", maxBytes)
		}
		line = append(line, fragment...)

		switch err {
		case nil:
			// Complete line.
		case bufio.ErrBufferFull:
			continue
		default:
			return nil, err
		}
		break
	}

	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return nil, fmt.Errorf("received empty message")
	}

	return line, nil
}

func WriteMessage(writer io.Writer, message any) error {
	payload, err := json.Marshal(message)
	if err != nil {
		return err
	}

	payload = append(payload, '\n')
	return writeAll(writer, payload)
}

func writeAll(writer io.Writer, payload []byte) error {
	for len(payload) > 0 {
		written, err := writer.Write(payload)
		if written > 0 {
			payload = payload[written:]
		}
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrNoProgress
		}
	}
	return nil
}

var connCounter atomic.Uint64

func NewConnID() string {
	return NewID("conn")
}

func NewTunnelID() string {
	return NewID("tun")
}

func NewID(prefix string) string {
	return fmt.Sprintf("%s-%d-%d", prefix, time.Now().UnixNano(), connCounter.Add(1))
}

func NormalizeProtocol(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", NetworkTCP:
		return NetworkTCP
	case NetworkUDP:
		return NetworkUDP
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func IsSupportedProtocol(value string) bool {
	switch NormalizeProtocol(value) {
	case NetworkTCP, NetworkUDP:
		return true
	default:
		return false
	}
}
