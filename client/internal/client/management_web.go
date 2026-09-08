package client

import (
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"tunnel/internal/common"
)

const maxManagedMappings = 20

type managementServerView struct {
	Custom     bool
	ID         string
	Name       string
	Address    string
	Selected   bool
	TLSLabel   string
	ServerName string
}

type managementMappingView struct {
	Index        int
	Name         string
	Protocol     string
	LocalHost    string
	LocalPort    string
	PublicTarget string
	Status       string
}

type managementPageData struct {
	Revision        uint64
	Version         string
	CSRFToken       string
	ListenAddress   string
	ConfigPath      string
	RuntimeStatus   string
	RuntimeError    string
	Connected       bool
	ConnectedAt     string
	ServerAddress   string
	NodeName        string
	CredentialSaved bool
	MappingCount    int
	Servers         []managementServerView
	Mappings        []managementMappingView
}

type managedMappingRequest struct {
	Name      string `json:"name"`
	Protocol  string `json:"protocol"`
	LocalHost string `json:"local_host"`
	LocalPort int    `json:"local_port"`
}

type managedConfigRequest struct {
	Revision     *uint64                 `json:"revision,omitempty"`
	ServerID     string                  `json:"server_id"`
	NodeName     string                  `json:"node_name"`
	Credential   string                  `json:"credential"`
	MappingCount int                     `json:"mapping_count"`
	Mappings     []managedMappingRequest `json:"mappings"`
}

type managedMappingStatus struct {
	Name         string `json:"name"`
	Protocol     string `json:"protocol"`
	LocalAddr    string `json:"local_addr"`
	PublicTarget string `json:"public_target"`
	Status       string `json:"status"`
}

type managedStatusResponse struct {
	Revision           uint64                 `json:"revision"`
	OK                 bool                   `json:"ok"`
	Version            string                 `json:"version"`
	Status             string                 `json:"status"`
	Connected          bool                   `json:"connected"`
	ConnectedAt        string                 `json:"connected_at,omitempty"`
	LastError          string                 `json:"last_error,omitempty"`
	ServerAddr         string                 `json:"server_addr"`
	NodeName           string                 `json:"node_name"`
	AssignmentsUpdated string                 `json:"assignments_updated,omitempty"`
	Mappings           []managedMappingStatus `json:"mappings"`
}

func clientVersion() string {
	return Version
}

func newClientCSRFToken() (string, error) {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}

func validateClientWebListenAddr(addr string) error {
	addr = strings.TrimSpace(addr)
	if clientWebDisabled(addr) {
		return nil
	}
	host, portText, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("web_listen_addr 必须使用 host:port 格式: %w", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("web_listen_addr 端口无效")
	}
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("web_listen_addr 仅允许绑定 127.0.0.1、::1 或 localhost")
	}
	return nil
}

func (c *Client) handleManagementDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" && r.URL.Path != "/api/view" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	cfg := c.currentConfig()
	status := c.managementStatusSnapshot()
	assignments := make(map[string]managedMappingStatus, len(status.Mappings))
	for _, mapping := range status.Mappings {
		assignments[strings.ToLower(mapping.Name)] = mapping
	}

	servers := make([]managementServerView, 0)
	selectedID := selectedHostedServerID(cfg)
	for _, server := range hostedServerCatalog(cfg) {
		tlsLabel := "明文"
		if server.TLSEnabled {
			tlsLabel = "TLS 1.3"
		}
		servers = append(servers, managementServerView{
			ID:         server.ID,
			Name:       server.Name,
			Address:    server.ServerAddr,
			Selected:   server.ID == selectedID,
			TLSLabel:   tlsLabel,
			ServerName: server.TLSServerName,
			Custom:     server.ID != primaryHostedServerID && server.ID != "current-custom",
		})
	}

	mappings := make([]managementMappingView, 0, len(cfg.Presets))
	for index, preset := range cfg.Presets {
		host, port := splitLocalAddress(preset.LocalAddr)
		view := managementMappingView{
			Index:        index + 1,
			Name:         preset.Name,
			Protocol:     common.NormalizeProtocol(preset.Protocol),
			LocalHost:    host,
			LocalPort:    port,
			PublicTarget: "等待服务端分配",
			Status:       "未连接",
		}
		if assignment, ok := assignments[strings.ToLower(preset.Name)]; ok {
			view.PublicTarget = assignment.PublicTarget
			view.Status = assignment.Status
		}
		mappings = append(mappings, view)
	}

	data := managementPageData{
		Revision:        c.currentRevision(),
		Version:         Version,
		CSRFToken:       c.webCSRFToken,
		ListenAddress:   cfg.WebListenAddr,
		ConfigPath:      cfg.configPath,
		RuntimeStatus:   status.Status,
		RuntimeError:    status.LastError,
		Connected:       status.Connected,
		ConnectedAt:     status.ConnectedAt,
		ServerAddress:   cfg.ServerAddr,
		NodeName:        cfg.NodeName,
		CredentialSaved: hasManagedCredential(cfg),
		MappingCount:    len(mappings),
		Servers:         servers,
		Mappings:        mappings,
	}

	setManagementSecurityHeaders(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := clientManagementTemplate.Execute(w, data); err != nil {
		http.Error(w, "render management panel failed", http.StatusInternalServerError)
	}
}

func (c *Client) handleManagementStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeClientJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	setManagementSecurityHeaders(w)
	writeClientJSON(w, http.StatusOK, c.managementStatusSnapshot())
}

func (c *Client) handleManagementConfig(w http.ResponseWriter, r *http.Request) {
	if err := c.validateManagementMutation(r); err != nil {
		writeClientJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var req managedConfigRequest
	if err := decodeSingleManagementJSON(decoder, &req); err != nil {
		writeClientJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "配置内容无效: " + err.Error()})
		return
	}

	c.updateMu.Lock()
	defer c.updateMu.Unlock()
	if req.Revision != nil && *req.Revision != c.currentRevision() {
		writeClientJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": "配置已在其他页面更新，请刷新后重试"})
		return
	}
	current := c.currentConfig()
	next, err := applyHostedServer(current, req.ServerID)
	if err != nil {
		writeClientJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	serverChanged := !sameHostedServerConnection(current, next)

	requestedNodeName := strings.TrimSpace(req.NodeName)
	nodeNameChanged := requestedNodeName != strings.TrimSpace(current.NodeName)
	next.NodeName = requestedNodeName
	if next.NodeName == "" || len(next.NodeName) > 128 {
		writeClientJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "节点名称不能为空且不能超过 128 个字符"})
		return
	}

	credential := strings.TrimSpace(req.Credential)
	if (serverChanged || nodeNameChanged) && credential == "" {
		writeClientJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "切换托管服务器或修改节点名称时，必须填写对应的连接 Token 或接入密码"})
		return
	}
	if !hasManagedCredential(next) && credential == "" {
		writeClientJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "首次配置必须填写连接 Token 或接入密码"})
		return
	}
	if credential != "" {
		next, err = applyManagedCredential(next, credential)
		if err != nil {
			writeClientJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
			return
		}
	}

	presets, err := managedPresetsFromRequest(req)
	if err != nil {
		writeClientJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	next.Presets = presets
	next.configPath = current.configPath
	if err := c.replaceConfigLocked(next); err != nil {
		writeClientJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "保存配置失败: " + err.Error()})
		return
	}

	writeClientJSON(w, http.StatusOK, map[string]any{
		"ok":            true,
		"revision":      c.currentRevision(),
		"node_name":     next.NodeName,
		"message":       "配置已保存，客户端正在重新连接",
		"mapping_count": len(presets),
	})
}

func (c *Client) handleManagementReconnect(w http.ResponseWriter, r *http.Request) {
	if err := c.validateManagementMutation(r); err != nil {
		writeClientJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	c.clearAssignments()
	c.setRuntimeState("正在重连", "", false)
	c.requestReconnect()
	writeClientJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "已请求立即重连"})
}

func (c *Client) validateManagementMutation(r *http.Request) error {
	if r.Method != http.MethodPost {
		return fmt.Errorf("method not allowed")
	}
	remoteHost, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || net.ParseIP(strings.Trim(remoteHost, "[]")) == nil || !net.ParseIP(strings.Trim(remoteHost, "[]")).IsLoopback() {
		return fmt.Errorf("只允许本机访问")
	}
	provided := strings.TrimSpace(r.Header.Get("X-1Cat-CSRF"))
	expected := strings.TrimSpace(c.webCSRFToken)
	if expected == "" || subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) != 1 {
		return fmt.Errorf("页面安全令牌无效，请刷新后重试")
	}
	if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" {
		parsed, parseErr := url.Parse(origin)
		if parseErr != nil || parsed.Scheme != "http" || !strings.EqualFold(parsed.Host, r.Host) {
			return fmt.Errorf("拒绝跨站配置请求")
		}
	}
	return nil
}

func (c *Client) managementStatusSnapshot() managedStatusResponse {
	cfg := c.currentConfig()
	c.runtimeMu.RLock()
	status := c.runtimeStatus
	lastError := c.runtimeError
	connectedAt := c.connectedAt
	assignmentsUpdated := c.assignmentsUpdated
	assignments := append([]common.AssignedTunnel(nil), c.assignments...)
	c.runtimeMu.RUnlock()

	assignmentByName := make(map[string]common.AssignedTunnel, len(assignments))
	for _, assignment := range assignments {
		assignmentByName[strings.ToLower(strings.TrimSpace(assignment.PresetName))] = assignment
	}
	mappings := make([]managedMappingStatus, 0, len(cfg.Presets))
	connected := !connectedAt.IsZero()
	for _, preset := range cfg.Presets {
		item := managedMappingStatus{
			Name:      preset.Name,
			Protocol:  strings.ToUpper(common.NormalizeProtocol(preset.Protocol)),
			LocalAddr: preset.LocalAddr,
			Status:    "客户端未连接",
		}
		if connected {
			item.Status = "等待服务端分配"
		}
		if assignment, ok := assignmentByName[strings.ToLower(strings.TrimSpace(preset.Name))]; ok && connected {
			item.PublicTarget = assignment.PublicTarget
			item.Status = "已分配"
		}
		mappings = append(mappings, item)
	}
	sort.Slice(mappings, func(i, j int) bool { return mappings[i].Name < mappings[j].Name })

	response := managedStatusResponse{
		Revision:   c.currentRevision(),
		OK:         true,
		Version:    Version,
		Status:     status,
		Connected:  connected,
		LastError:  lastError,
		ServerAddr: cfg.ServerAddr,
		NodeName:   cfg.NodeName,
		Mappings:   mappings,
	}
	if !connectedAt.IsZero() {
		response.ConnectedAt = connectedAt.Local().Format("2006-01-02 15:04:05")
	}
	if !assignmentsUpdated.IsZero() {
		response.AssignmentsUpdated = assignmentsUpdated.Local().Format("2006-01-02 15:04:05")
	}
	return response
}

func (c *Client) currentRevision() uint64 {
	c.cfgMu.RLock()
	defer c.cfgMu.RUnlock()
	return c.configRevision
}

func decodeSingleManagementJSON(decoder *json.Decoder, target any) error {
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("只允许一个 JSON 对象")
	}
	return nil
}

func managedPresetsFromRequest(req managedConfigRequest) ([]common.Preset, error) {
	if req.MappingCount < 1 || req.MappingCount > maxManagedMappings {
		return nil, fmt.Errorf("开放端口数量必须在 1-%d 之间", maxManagedMappings)
	}
	if len(req.Mappings) != req.MappingCount {
		return nil, fmt.Errorf("映射明细数量与开放端口数量不一致")
	}

	seen := make(map[string]struct{}, len(req.Mappings))
	presets := make([]common.Preset, 0, len(req.Mappings))
	for index, mapping := range req.Mappings {
		name := strings.TrimSpace(mapping.Name)
		if name == "" {
			name = fmt.Sprintf("port-%d", index+1)
		}
		if len(name) > 64 {
			return nil, fmt.Errorf("第 %d 条映射名称不能超过 64 个字符", index+1)
		}
		key := strings.ToLower(name)
		if _, exists := seen[key]; exists {
			return nil, fmt.Errorf("映射名称 %q 重复", name)
		}
		seen[key] = struct{}{}

		protocol := common.NormalizeProtocol(mapping.Protocol)
		if !common.IsSupportedProtocol(protocol) {
			return nil, fmt.Errorf("第 %d 条映射协议必须是 TCP 或 UDP", index+1)
		}
		host := strings.TrimSpace(mapping.LocalHost)
		host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
		if !validManagedLocalHost(host) {
			return nil, fmt.Errorf("第 %d 条映射的本机 IP/主机名无效", index+1)
		}
		if mapping.LocalPort < 1 || mapping.LocalPort > 65535 {
			return nil, fmt.Errorf("第 %d 条映射的本机端口必须在 1-65535 之间", index+1)
		}

		presets = append(presets, common.Preset{
			Name:        name,
			Protocol:    protocol,
			LocalAddr:   net.JoinHostPort(host, strconv.Itoa(mapping.LocalPort)),
			Description: "由本机 Web 管理面板配置",
		})
	}
	return presets, nil
}

func validManagedLocalHost(host string) bool {
	host = strings.TrimSpace(host)
	if host == "" || len(host) > 253 || strings.ContainsAny(host, " \t\r\n/") {
		return false
	}
	if net.ParseIP(host) != nil {
		return true
	}
	if strings.Contains(host, ":") {
		return false
	}
	host = strings.TrimSuffix(host, ".")
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, char := range label {
			if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
				(char >= '0' && char <= '9') || char == '-' || char == '_' {
				continue
			}
			return false
		}
	}
	return true
}

func applyManagedCredential(cfg Config, credential string) (Config, error) {
	credential = strings.TrimSpace(credential)
	cfg.BootstrapToken = ""
	cfg.Token = ""
	cfg.EnrollmentPassword = ""
	cfg.ProtectedBootstrap = ""
	cfg.ProtectedToken = ""
	cfg.ProtectedEnrollment = ""

	payload, err := common.DecodeBootstrapToken(credential)
	if err == nil {
		if !strings.EqualFold(payload.ServerAddr, cfg.ServerAddr) {
			return Config{}, fmt.Errorf("连接 Token 属于服务器 %s，与当前选择的 %s 不一致", payload.ServerAddr, cfg.ServerAddr)
		}
		cfg.BootstrapToken = credential
		cfg.Token = payload.AccessToken
		if payload.NodeName != "" {
			cfg.NodeName = payload.NodeName
		}
		return cfg, nil
	}
	if strings.HasPrefix(credential, "1cat1.") {
		return Config{}, fmt.Errorf("连接 Token 无效: %v", err)
	}

	cfg.Token = credential
	cfg.EnrollmentPassword = credential
	return cfg, nil
}

func sameHostedServerConnection(a, b Config) bool {
	return strings.EqualFold(strings.TrimSpace(a.ServerAddr), strings.TrimSpace(b.ServerAddr)) &&
		a.TLSEnabled == b.TLSEnabled &&
		strings.EqualFold(strings.TrimSpace(a.TLSServerName), strings.TrimSpace(b.TLSServerName)) &&
		strings.EqualFold(strings.TrimSpace(a.TLSCAFile), strings.TrimSpace(b.TLSCAFile))
}

func hasManagedCredential(cfg Config) bool {
	return strings.TrimSpace(cfg.BootstrapToken) != "" || strings.TrimSpace(cfg.Token) != "" || strings.TrimSpace(cfg.EnrollmentPassword) != ""
}

func setManagementSecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
}

//go:embed management.js
var clientManagementJavaScript string

var clientManagementTemplate = template.Must(template.New("client-management").Parse(`
<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <meta name="csrf-token" content="{{.CSRFToken}}">
  <meta name="config-revision" content="{{.Revision}}">
  <title>1CatTunnel 本机管理</title>
  <style>
    :root{--ink:#17211b;--muted:#667169;--paper:#fffdf8;--line:#d8ded7;--green:#186b49;--green-soft:#e3f3e9;--amber:#d58220;--amber-soft:#fff0d8;--red:#a43b35;--shadow:0 18px 50px rgba(31,45,35,.10)}
    *{box-sizing:border-box}body{margin:0;color:var(--ink);font-family:"Bahnschrift","Microsoft YaHei UI",sans-serif;background:radial-gradient(circle at 12% 8%,#f9d8a8 0,transparent 28%),radial-gradient(circle at 88% 20%,#b8dfc7 0,transparent 30%),#eef1e9;min-height:100vh}
    body:before{content:"";position:fixed;inset:0;pointer-events:none;opacity:.22;background-image:linear-gradient(90deg,rgba(35,60,44,.09) 1px,transparent 1px),linear-gradient(rgba(35,60,44,.09) 1px,transparent 1px);background-size:32px 32px}
    .shell{position:relative;max-width:1180px;margin:0 auto;padding:30px 20px 60px}.hero{display:flex;justify-content:space-between;gap:20px;align-items:flex-start;margin-bottom:18px}.eyebrow{font-size:12px;letter-spacing:.18em;text-transform:uppercase;color:var(--green);font-weight:800}.hero h1{font-family:Georgia,"Microsoft YaHei UI",serif;font-size:clamp(32px,5vw,58px);line-height:1;margin:8px 0 12px}.subtitle{max-width:720px;color:var(--muted);line-height:1.8}.status-pill{white-space:nowrap;padding:10px 14px;border-radius:999px;background:var(--amber-soft);color:#7c4d16;font-weight:800;border:1px solid #efc88f}.status-pill.online{background:var(--green-soft);color:var(--green);border-color:#b8dec8}
    .card{background:rgba(255,253,248,.94);border:1px solid rgba(216,222,215,.95);border-radius:22px;padding:20px;margin-bottom:16px;box-shadow:var(--shadow);backdrop-filter:blur(12px)}.card h2{font-family:Georgia,"Microsoft YaHei UI",serif;margin:0 0 8px;font-size:23px}.hint{color:var(--muted);font-size:13px;line-height:1.7}.grid{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:12px;margin-top:14px}.metric{padding:14px;border:1px solid var(--line);border-radius:16px;background:#fbfcf7}.metric strong{display:block;margin-top:6px;font-size:18px;word-break:break-all}.error{color:var(--red);white-space:pre-wrap}.notice{display:none;margin:12px 0 0;padding:11px 13px;border-radius:12px;background:var(--green-soft);color:var(--green)}.notice.show{display:block}.notice.bad{background:#fbe7e5;color:var(--red)}
    label{display:block;font-size:13px;font-weight:800;margin-bottom:7px}input,select{width:100%;border:1px solid var(--line);border-radius:12px;background:#fff;padding:11px 12px;color:var(--ink);font:inherit}input:focus,select:focus{outline:3px solid rgba(24,107,73,.14);border-color:var(--green)}.form-grid{display:grid;grid-template-columns:1.2fr 1fr 1fr;gap:14px;margin-top:16px}.mapping-row{border:1px solid var(--line);border-radius:18px;padding:14px;margin-top:12px;background:linear-gradient(135deg,#fff 0,#f7faf4 100%)}.mapping-head{display:flex;justify-content:space-between;gap:12px;align-items:center;margin-bottom:10px}.mapping-title{font-weight:900}.mapping-public{font-size:13px;color:var(--green);font-weight:800}.mapping-grid{display:grid;grid-template-columns:1.1fr .7fr 1fr .65fr;gap:10px}.actions{display:flex;gap:10px;flex-wrap:wrap;margin-top:18px}button{border:0;border-radius:12px;padding:11px 16px;background:var(--green);color:#fff;font-weight:900;cursor:pointer}button.secondary{background:#33463a}button:disabled{opacity:.55;cursor:wait}.footer{color:var(--muted);font-size:12px;line-height:1.8;margin-top:18px}.mono{font-family:Consolas,"Courier New",monospace}
    .inline-controls{display:flex;gap:8px;align-items:center}.inline-controls input{min-width:0}.inline-controls button{flex-shrink:0}.mapping-head{flex-wrap:wrap}.mapping-public{overflow-wrap:anywhere}.server-tools{margin-top:14px;border-top:1px solid var(--line);padding-top:14px}.server-tools summary{cursor:pointer;font-weight:800}.server-entry{display:flex;justify-content:space-between;align-items:center;gap:12px;padding:9px 0;border-bottom:1px solid var(--line);overflow-wrap:anywhere}.count-controls{max-width:340px}.danger{background:#a43b35}.dirty{color:#945600}.copy-button{padding:6px 10px;font-size:12px}.hidden{display:none!important}
    @media(max-width:840px){.hero{flex-direction:column}.grid,.form-grid,.mapping-grid{grid-template-columns:1fr}.status-pill{align-self:flex-start}.count-section{grid-template-columns:1fr!important}.server-entry{align-items:flex-start}.shell{padding:20px 12px 40px}}
  </style>
</head>
<body>
  <main class="shell">
    <header class="hero">
      <div><div class="eyebrow">本机控制中心 · {{.Version}}</div><h1>1CatTunnel 本机管理</h1><div class="subtitle">只选择需要的公网入口数量，并填写每个入口对应的本机服务。公网端口号由所选托管服务器随机分配，客户端不会占用或指定公网端口。</div></div>
      <div id="runtime-pill" class="status-pill {{if .Connected}}online{{end}}">{{.RuntimeStatus}}</div>
    </header>

    <section class="card">
      <h2>运行状态</h2>
      <div class="grid">
        <div class="metric"><span class="hint">当前托管服务器</span><strong id="status-server" class="mono">{{.ServerAddress}}</strong></div>
        <div class="metric"><span class="hint">节点名称</span><strong id="status-node">{{.NodeName}}</strong></div>
        <div class="metric"><span class="hint">已分配 / 申请数量</span><strong id="status-count">0 / {{.MappingCount}}</strong></div>
      </div>
      <div id="runtime-error" class="hint error" style="margin-top:12px">{{.RuntimeError}}</div>
      <div id="notice" class="notice" role="status" aria-live="polite"></div>
    </section>

    <section class="card">
      <h2>连接与映射配置</h2>
      <div class="hint">保存后客户端会自动热重连。只调整映射时，连接凭据留空会保留已保存凭据；切换服务器或修改节点名称时必须重新填写对应凭据。</div>
      <div class="form-grid">
        <div><label for="server-id">托管服务器</label><select id="server-id">{{range .Servers}}<option value="{{.ID}}" data-address="{{.Address}}" {{if .Selected}}selected{{end}}>{{.Name}} · {{.Address}} · {{.TLSLabel}}</option>{{end}}</select></div>
        <div><label for="node-name">节点名称</label><input id="node-name" maxlength="128" value="{{.NodeName}}" placeholder="例如：office-linux-01"></div>
        <div><label for="credential">连接 Token / 接入密码</label><div class="inline-controls"><input id="credential" type="password" autocomplete="new-password" placeholder="{{if .CredentialSaved}}已保存，留空保持不变{{else}}首次配置必须填写{{end}}"><button id="toggle-credential" class="secondary" type="button" aria-pressed="false">显示</button></div></div>
      </div>
      <details class="server-tools"><summary>管理托管服务器</summary>
        <div class="hint">可提前添加后续节点。使用该服务器的控制端口；新服务器默认启用 TLS 验证。新增后从上面的列表选择，再填写该节点的接入凭据。</div>
        <div id="server-list">{{range .Servers}}{{if .Custom}}<div class="server-entry" data-server-id="{{.ID}}"><span>{{.Name}} · {{.Address}}</span><button class="secondary remove-server" type="button">删除</button></div>{{end}}{{end}}</div>
        <div class="form-grid"><div><label for="new-server-name">显示名称</label><input id="new-server-name" maxlength="128" placeholder="例如：广州节点"></div><div><label for="new-server-address">控制地址</label><input id="new-server-address" placeholder="tunnel.example.com:50001"></div><div><label for="new-server-tls">证书域名（可留空）</label><input id="new-server-tls" placeholder="默认使用地址中的主机名"></div></div>
        <div class="actions"><button id="add-server" type="button">添加到服务器列表</button></div>
      </details>
      <div class="form-grid count-section" style="grid-template-columns:340px 1fr">
        <div><label for="mapping-count">需要开放的公网端口数量（1–20）</label><div class="inline-controls count-controls"><button id="less-mapping" class="secondary" type="button" aria-label="减少映射数量">−</button><input id="mapping-count" type="number" min="1" max="20" step="1" value="{{.MappingCount}}"><button id="more-mapping" class="secondary" type="button" aria-label="增加映射数量">＋</button></div></div>
        <div class="hint" style="align-self:end;padding-bottom:10px">每条映射获得一个服务端分配的公网端口。减少数量会移除末尾映射，点击“保存并应用”后才生效。<br>本机服务需已启动，SSH 通常填 22，网页服务填实际监听端口。</div>
      </div>

      <div id="mapping-list">
        {{range .Mappings}}
        <div class="mapping-row">
          <div class="mapping-head"><div class="mapping-title">映射 <span data-role="index">{{.Index}}</span></div><div class="mapping-public" data-role="public">{{.PublicTarget}} · {{.Status}}</div></div>
          <div class="mapping-grid">
            <div><label>映射名称</label><input class="mapping-name" maxlength="64" value="{{.Name}}" placeholder="例如 ssh"></div>
            <div><label>协议</label><select class="mapping-protocol"><option value="tcp" {{if eq .Protocol "tcp"}}selected{{end}}>TCP</option><option value="udp" {{if eq .Protocol "udp"}}selected{{end}}>UDP</option></select></div>
            <div><label>本机 IP / 主机名</label><input class="mapping-host" value="{{.LocalHost}}" placeholder="127.0.0.1"></div>
            <div><label>本机端口</label><input class="mapping-port" type="number" min="1" max="65535" value="{{.LocalPort}}" placeholder="22"></div>
          </div>
        </div>
        {{end}}
      </div>
      <template id="mapping-template"><div class="mapping-row"><div class="mapping-head"><div class="mapping-title">映射 <span data-role="index"></span></div><div class="mapping-public" data-role="public">等待服务端分配</div></div><div class="mapping-grid"><div><label>映射名称</label><input class="mapping-name" maxlength="64"></div><div><label>协议</label><select class="mapping-protocol"><option value="tcp">TCP</option><option value="udp">UDP</option></select></div><div><label>本机 IP / 主机名</label><input class="mapping-host" value="127.0.0.1"></div><div><label>本机端口</label><input class="mapping-port" type="number" min="1" max="65535"></div></div></div></template>
      <div class="actions"><button id="save-button" type="button">保存并应用</button><button id="reconnect-button" class="secondary" type="button">立即重连</button><span id="unsaved" class="hint dirty hidden">有未保存的修改</span></div>
    </section>

    <div class="footer">管理地址：<span class="mono">http://{{.ListenAddress}}/</span><br>配置文件：<span class="mono">{{.ConfigPath}}</span><br>页面仅允许本机回环访问，不会显示已保存的 Token 或密码。</div>
  </main>
  <script src="/assets/management.js" defer></script>
</body>
</html>
`))
