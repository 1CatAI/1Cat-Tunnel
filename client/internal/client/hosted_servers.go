package client

import (
	"fmt"
	"strings"
)

const primaryHostedServerID = "1cat-primary"

type HostedServer struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	ServerAddr    string `json:"server_addr"`
	TLSEnabled    bool   `json:"tls_enabled"`
	TLSServerName string `json:"tls_server_name,omitempty"`
	TLSCAFile     string `json:"tls_ca_file,omitempty"`
}

var builtInHostedServers = []HostedServer{
	{
		ID:            primaryHostedServerID,
		Name:          "1Cat 主节点",
		ServerAddr:    defaultPublicServerAddr,
		TLSEnabled:    true,
		TLSServerName: "dx.1catai.com",
		TLSCAFile:     "1cat-tunnel-ca.pem",
	},
}

func hostedServerCatalog(cfg Config) []HostedServer {
	servers := make([]HostedServer, 0, len(builtInHostedServers)+len(cfg.HostedServers)+1)
	servers = append(servers, builtInHostedServers...)
	servers = append(servers, normalizeHostedServers(cfg.HostedServers)...)

	seen := make(map[string]struct{}, len(servers))
	out := make([]HostedServer, 0, len(servers)+1)
	for _, server := range servers {
		if _, exists := seen[server.ID]; exists {
			continue
		}
		seen[server.ID] = struct{}{}
		out = append(out, server)
	}

	if strings.TrimSpace(cfg.ServerAddr) != "" && hostedServerIDForAddress(out, cfg.ServerAddr) == "" {
		out = append(out, HostedServer{
			ID:            "current-custom",
			Name:          "当前自定义服务器",
			ServerAddr:    strings.TrimSpace(cfg.ServerAddr),
			TLSEnabled:    cfg.TLSEnabled,
			TLSServerName: strings.TrimSpace(cfg.TLSServerName),
			TLSCAFile:     strings.TrimSpace(cfg.TLSCAFile),
		})
	}
	return out
}

func normalizeHostedServers(input []HostedServer) []HostedServer {
	out := make([]HostedServer, 0, len(input))
	for _, server := range input {
		server.ID = strings.TrimSpace(server.ID)
		server.Name = strings.TrimSpace(server.Name)
		server.ServerAddr = strings.TrimSpace(server.ServerAddr)
		server.TLSServerName = strings.TrimSpace(server.TLSServerName)
		server.TLSCAFile = strings.TrimSpace(server.TLSCAFile)
		if server.ID == "" || server.Name == "" || server.ServerAddr == "" {
			continue
		}
		out = append(out, server)
	}
	return out
}

func selectedHostedServerID(cfg Config) string {
	servers := hostedServerCatalog(cfg)
	if id := strings.TrimSpace(cfg.SelectedServerID); id != "" {
		for _, server := range servers {
			if server.ID == id && strings.EqualFold(server.ServerAddr, strings.TrimSpace(cfg.ServerAddr)) {
				return id
			}
		}
	}
	if id := hostedServerIDForAddress(servers, cfg.ServerAddr); id != "" {
		return id
	}
	if len(servers) > 0 {
		return servers[0].ID
	}
	return ""
}

func hostedServerIDForAddress(servers []HostedServer, address string) string {
	address = strings.TrimSpace(address)
	for _, server := range servers {
		if strings.EqualFold(server.ServerAddr, address) {
			return server.ID
		}
	}
	return ""
}

func applyHostedServer(cfg Config, id string) (Config, error) {
	id = strings.TrimSpace(id)
	for _, server := range hostedServerCatalog(cfg) {
		if server.ID != id {
			continue
		}
		// Keep the current TLS trust settings during ordinary mapping edits.
		if id == selectedHostedServerID(cfg) && strings.EqualFold(server.ServerAddr, cfg.ServerAddr) {
			cfg.SelectedServerID = id
			return cfg, nil
		}
		cfg.SelectedServerID = server.ID
		cfg.ServerAddr = server.ServerAddr
		cfg.TLSEnabled = server.TLSEnabled
		cfg.TLSServerName = server.TLSServerName
		cfg.TLSCAFile = server.TLSCAFile
		return cfg, nil
	}
	return Config{}, fmt.Errorf("未知托管服务器 %q", id)
}
