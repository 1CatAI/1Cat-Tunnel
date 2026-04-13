package server

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"tunnel/internal/common"
)

type persistentState struct {
	Version string            `json:"version"`
	SavedAt int64             `json:"saved_at"`
	Nodes   []persistedNode   `json:"nodes"`
	Tunnels []persistedTunnel `json:"tunnels"`
}

type persistedNode struct {
	Name                 string          `json:"name"`
	AccessToken          string          `json:"access_token"`
	CredentialIssuedAt   int64           `json:"credential_issued_at"`
	Hostname             string          `json:"hostname,omitempty"`
	Platform             string          `json:"platform,omitempty"`
	Remote               string          `json:"remote,omitempty"`
	Presets              []common.Preset `json:"presets,omitempty"`
	ConnectedAt          int64           `json:"connected_at,omitempty"`
	LastSeen             int64           `json:"last_seen,omitempty"`
	LastProvisionError   string          `json:"last_provision_error,omitempty"`
	LastProvisionErrorAt int64           `json:"last_provision_error_at,omitempty"`
}

type persistedTunnel struct {
	ID               string `json:"id"`
	NodeName         string `json:"node_name"`
	PresetName       string `json:"preset_name"`
	Protocol         string `json:"protocol"`
	PublicPort       int    `json:"public_port"`
	CreatedAt        int64  `json:"created_at"`
	TotalConnections uint64 `json:"total_connections"`
	IngressBytes     uint64 `json:"ingress_bytes"`
	EgressBytes      uint64 `json:"egress_bytes"`
	LastActivityUnix int64  `json:"last_activity_unix"`
	LastError        string `json:"last_error,omitempty"`
	LastErrorAt      int64  `json:"last_error_at,omitempty"`
}

func (s *Server) restoreState() error {
	state, err := loadPersistentState(s.cfg.StateFile)
	if err != nil {
		return err
	}

	for _, item := range state.Nodes {
		name := strings.TrimSpace(item.Name)
		accessToken := strings.TrimSpace(item.AccessToken)
		if name == "" || accessToken == "" {
			continue
		}

		node := &Node{
			name:               name,
			hostname:           strings.TrimSpace(item.Hostname),
			platform:           strings.TrimSpace(item.Platform),
			remote:             strings.TrimSpace(item.Remote),
			accessToken:        accessToken,
			presets:            make(map[string]common.Preset),
			lastProvisionError: strings.TrimSpace(item.LastProvisionError),
		}
		if item.CredentialIssuedAt > 0 {
			node.credentialIssuedAt = time.Unix(item.CredentialIssuedAt, 0)
		}
		if item.ConnectedAt > 0 {
			node.connectedAt = time.Unix(item.ConnectedAt, 0)
		}
		if item.LastSeen > 0 {
			node.lastSeen = time.Unix(item.LastSeen, 0)
		}
		if item.LastProvisionErrorAt > 0 {
			node.lastProvisionErrorAt = time.Unix(item.LastProvisionErrorAt, 0)
		}

		for _, preset := range item.Presets {
			preset.Name = strings.TrimSpace(preset.Name)
			preset.LocalAddr = strings.TrimSpace(preset.LocalAddr)
			preset.Protocol = common.NormalizeProtocol(preset.Protocol)
			if preset.Name == "" || preset.LocalAddr == "" || !common.IsSupportedProtocol(preset.Protocol) {
				continue
			}
			node.presets[preset.Name] = preset
		}

		s.nodes[node.name] = node
	}

	for _, item := range state.Tunnels {
		node := s.nodes[strings.TrimSpace(item.NodeName)]
		if node == nil {
			continue
		}
		if _, ok := node.presets[item.PresetName]; !ok {
			continue
		}

		tunnel, err := s.createTunnelInternal(item.NodeName, item.PresetName, item.PublicPort, "")
		if err != nil {
			log.Printf("restore tunnel for node=%s preset=%s port=%d failed: %v", item.NodeName, item.PresetName, item.PublicPort, err)
			node.setProvisionError("恢复已保存映射失败: " + err.Error())
			continue
		}

		tunnel.applyPersistedStats(item)
	}

	return nil
}

func (s *Server) saveStateQuiet(reason string) {
	if err := s.saveState(); err != nil {
		log.Printf("save state failed (%s): %v", reason, err)
	}
}

func (s *Server) saveState() error {
	if strings.TrimSpace(s.cfg.StateFile) == "" {
		return nil
	}

	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	state := s.snapshotState()
	return writePersistentState(s.cfg.StateFile, state)
}

func (s *Server) snapshotState() persistentState {
	s.mu.RLock()
	nodes := make([]persistedNode, 0, len(s.nodes))
	for _, node := range s.nodes {
		node.mu.RLock()
		presets := make([]common.Preset, 0, len(node.presets))
		for _, preset := range node.presets {
			presets = append(presets, common.Preset{
				Name:        preset.Name,
				LocalAddr:   preset.LocalAddr,
				Description: preset.Description,
				Protocol:    common.NormalizeProtocol(preset.Protocol),
			})
		}
		nodeState := persistedNode{
			Name:                 node.name,
			AccessToken:          node.accessToken,
			Hostname:             node.hostname,
			Platform:             node.platform,
			Remote:               node.remote,
			Presets:              presets,
			LastProvisionError:   node.lastProvisionError,
			CredentialIssuedAt:   unixIfNotZero(node.credentialIssuedAt),
			ConnectedAt:          unixIfNotZero(node.connectedAt),
			LastSeen:             unixIfNotZero(node.lastSeen),
			LastProvisionErrorAt: unixIfNotZero(node.lastProvisionErrorAt),
		}
		node.mu.RUnlock()
		nodes = append(nodes, nodeState)
	}

	tunnels := make([]persistedTunnel, 0, len(s.tunnels))
	for _, tunnel := range s.tunnels {
		lastError, lastErrorAt := tunnel.lastErrorSnapshot()
		tunnels = append(tunnels, persistedTunnel{
			ID:               tunnel.id,
			NodeName:         tunnel.nodeName,
			PresetName:       tunnel.presetName,
			Protocol:         tunnel.protocol,
			PublicPort:       tunnel.publicPort,
			CreatedAt:        unixIfNotZero(tunnel.createdAt),
			TotalConnections: tunnel.totalConnections.Load(),
			IngressBytes:     tunnel.ingressBytes.Load(),
			EgressBytes:      tunnel.egressBytes.Load(),
			LastActivityUnix: tunnel.lastActivityUnix.Load(),
			LastError:        lastError,
			LastErrorAt:      unixIfNotZero(lastErrorAt),
		})
	}
	s.mu.RUnlock()

	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].Name < nodes[j].Name
	})
	sort.Slice(tunnels, func(i, j int) bool {
		if tunnels[i].PublicPort == tunnels[j].PublicPort {
			return tunnels[i].PresetName < tunnels[j].PresetName
		}
		return tunnels[i].PublicPort < tunnels[j].PublicPort
	})

	return persistentState{
		Version: common.Version,
		SavedAt: time.Now().Unix(),
		Nodes:   nodes,
		Tunnels: tunnels,
	}
}

func loadPersistentState(path string) (persistentState, error) {
	if strings.TrimSpace(path) == "" {
		return persistentState{Version: common.Version}, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return persistentState{Version: common.Version}, nil
		}
		return persistentState{}, err
	}
	if len(data) == 0 {
		return persistentState{Version: common.Version}, nil
	}

	var state persistentState
	if err := json.Unmarshal(data, &state); err != nil {
		return persistentState{}, err
	}
	if strings.TrimSpace(state.Version) == "" {
		state.Version = common.Version
	}
	return state, nil
}

func writePersistentState(path string, state persistentState) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	payload, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')

	tempPath := path + ".tmp"
	if err := os.WriteFile(tempPath, payload, 0o644); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}

func unixIfNotZero(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.Unix()
}
