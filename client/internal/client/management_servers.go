package client

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
)

func localManagementOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setManagementSecurityHeaders(w)
		if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+r.Host {
			http.Error(w, "禁止跨来源访问本机管理接口", http.StatusForbidden)
			return
		}
		peer, _, err := net.SplitHostPort(r.RemoteAddr)
		peerIP := net.ParseIP(peer)
		host := r.Host
		if name, _, splitErr := net.SplitHostPort(host); splitErr == nil {
			host = name
		}
		host = strings.Trim(host, "[]")
		hostIP := net.ParseIP(host)
		if err != nil || peerIP == nil || !peerIP.IsLoopback() ||
			(!strings.EqualFold(host, "localhost") && (hostIP == nil || !hostIP.IsLoopback())) {
			http.Error(w, "管理页面只允许使用本机回环地址访问", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type managedServerRequest struct {
	Action        string `json:"action"`
	ID            string `json:"id,omitempty"`
	Name          string `json:"name,omitempty"`
	ServerAddr    string `json:"server_addr,omitempty"`
	TLSServerName string `json:"tls_server_name,omitempty"`
}

func (c *Client) handleManagementServers(w http.ResponseWriter, r *http.Request) {
	if err := c.validateManagementMutation(r); err != nil {
		writeClientJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	decoder.DisallowUnknownFields()
	var req managedServerRequest
	if err := decodeSingleManagementJSON(decoder, &req); err != nil {
		writeClientJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "服务器信息无效"})
		return
	}
	c.updateMu.Lock()
	defer c.updateMu.Unlock()
	cfg := c.currentConfig()
	next, selected, err := editHostedServers(cfg, req)
	if err == nil {
		if cfg.configPath == "" {
			err = fmt.Errorf("客户端配置路径不可用")
		} else {
			err = SaveConfig(cfg.configPath, next)
		}
	}
	if err != nil {
		writeClientJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	c.cfgMu.Lock()
	c.cfg = next
	c.cfgMu.Unlock()
	writeClientJSON(w, http.StatusOK, map[string]any{"ok": true, "server": selected, "message": "服务器列表已保存"})
}

func editHostedServers(cfg Config, req managedServerRequest) (Config, *HostedServer, error) {
	cfg.HostedServers = append([]HostedServer(nil), cfg.HostedServers...)
	if req.Action == "remove" {
		if req.ID == selectedHostedServerID(cfg) {
			return Config{}, nil, fmt.Errorf("请先切换到其他服务器，再删除当前服务器")
		}
		for index, server := range cfg.HostedServers {
			if server.ID == req.ID {
				cfg.HostedServers = append(cfg.HostedServers[:index], cfg.HostedServers[index+1:]...)
				return cfg, nil, nil
			}
		}
		return Config{}, nil, fmt.Errorf("仅可删除自行添加的服务器")
	}
	if req.Action != "add" {
		return Config{}, nil, fmt.Errorf("未知的服务器操作")
	}
	name, address := strings.TrimSpace(req.Name), strings.TrimSpace(req.ServerAddr)
	host, portText, err := net.SplitHostPort(address)
	port, portErr := strconv.Atoi(portText)
	if name == "" || len(name) > 128 || err != nil || portErr != nil || port < 1 || port > 65535 || !validManagedLocalHost(host) {
		return Config{}, nil, fmt.Errorf("请填写服务器名称和正确的控制地址，例如 tunnel.example.com:50001")
	}
	serverName := strings.TrimSpace(req.TLSServerName)
	if serverName == "" {
		serverName = host
	}
	if !validManagedLocalHost(serverName) {
		return Config{}, nil, fmt.Errorf("证书域名无效")
	}
	for _, server := range hostedServerCatalog(cfg) {
		if strings.EqualFold(server.ServerAddr, address) && strings.EqualFold(server.TLSServerName, serverName) {
			return Config{}, nil, fmt.Errorf("该服务器已在列表中，请直接选择")
		}
	}
	if len(cfg.HostedServers) >= 32 {
		return Config{}, nil, fmt.Errorf("最多保存 32 个自定义服务器")
	}
	digest := sha256.Sum256([]byte(strings.ToLower(address + "|" + serverName)))
	server := HostedServer{
		ID: "custom-" + hex.EncodeToString(digest[:8]), Name: name, ServerAddr: address,
		TLSEnabled: true, TLSServerName: serverName,
	}
	cfg.HostedServers = append(cfg.HostedServers, server)
	return cfg, &server, nil
}
