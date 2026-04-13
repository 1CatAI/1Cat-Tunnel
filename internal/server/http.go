package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"tunnel/internal/common"
)

const sessionCookieName = "tunnel_session"

type dashboardFlash struct {
	Message            string
	Error              string
	IssuedNodeName     string
	IssuedConnectToken string
}

type webSession struct {
	ExpireAt               time.Time
	Flash                  dashboardFlash
	LastIssuedNodeName     string
	LastIssuedConnectToken string
}

type dashboardData struct {
	Version            string
	RefreshedAt        string
	Message            string
	Error              string
	IssuedNodeName     string
	IssuedConnectToken string
	IssuedTokenMasked  string
	Summary            dashboardSummary
	ControlAddress     string
	WebURL             string
	PortRange          string
	StateFile          string
	Nodes              []dashboardNode
	Tunnels            []dashboardTunnel
}

type dashboardSummary struct {
	TotalNodes        int
	OnlineNodes       int
	OfflineNodes      int
	PublishedTunnels  int
	AvailablePorts    int
	PortUsage         string
	ActiveConnections int64
	TotalConnections  uint64
	IngressBytes      string
	EgressBytes       string
}

type dashboardNode struct {
	Name               string
	Hostname           string
	Platform           string
	Remote             string
	StatusText         string
	StatusClass        string
	ConnectedSince     string
	LastSeen           string
	CredentialIssuedAt string
	ProvisionError     string
	PresetCount        int
	PublishedCount     int
	ActiveConnections  int64
	TotalConnections   uint64
	IngressBytes       string
	EgressBytes        string
	Endpoints          []dashboardEndpoint
}

type dashboardEndpoint struct {
	TunnelID           string
	PresetName         string
	Protocol           string
	LocalAddr          string
	PublicTarget       string
	StatusText         string
	StatusClass        string
	CommandHint        string
	CreatedAt          string
	LastActivity       string
	LastError          string
	ActiveConnections  int64
	PendingConnections int
	TotalConnections   uint64
	IngressBytes       string
	EgressBytes        string
	Assigned           bool
}

type dashboardTunnel struct {
	ID                 string
	NodeName           string
	PresetName         string
	Protocol           string
	LocalAddr          string
	PublicTarget       string
	StatusText         string
	StatusClass        string
	CommandHint        string
	CreatedAt          string
	LastActivity       string
	LastError          string
	ActiveConnections  int64
	PendingConnections int
	TotalConnections   uint64
	IngressBytes       string
	EgressBytes        string
}

type commonPresetView struct {
	Name      string
	LocalAddr string
	Protocol  string
}

var dashboardTemplate = template.Must(template.New("dashboard").Parse(`
<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <title>{{.Version}} Console</title>
  <meta name="viewport" content="width=device-width, initial-scale=1">
  {{if not .IssuedConnectToken}}<meta http-equiv="refresh" content="5">{{end}}
  <style>
    body{margin:0;font-family:"Segoe UI","PingFang SC","Microsoft YaHei",sans-serif;background:#f3f6f3;color:#162118}
    .wrap{max-width:1380px;margin:0 auto;padding:22px}
    .card{background:#fff;border:1px solid #dbe4db;border-radius:16px;padding:16px;margin-bottom:16px;box-shadow:0 10px 28px rgba(0,0,0,.05)}
    .hero,.row{display:flex;justify-content:space-between;gap:12px;align-items:flex-start}
    .grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(180px,1fr));gap:12px}
    .node-grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(360px,1fr));gap:14px}
    .metric,.endpoint{border:1px solid #dbe4db;border-radius:14px;padding:12px;background:#fafcf9}
    .endpoint{margin-top:10px}
    .actions{display:flex;gap:8px;flex-wrap:wrap;margin-top:12px}
    .muted{color:#5f6e61;font-size:13px;line-height:1.7}
    .value{font-size:24px;font-weight:700}
    .mono{font-family:Consolas,"Courier New",monospace;word-break:break-all}
    .status{display:inline-block;padding:4px 10px;border-radius:999px;font-size:12px;font-weight:700;background:#e5f5ec;color:#1f7a52}
    .status.warn{background:#fff0e1;color:#a55a1d}
    .status.offline{background:#fde8e8;color:#943535}
    .notice{padding:12px 14px;border-radius:12px;margin-bottom:14px;background:#e8f6ee;color:#1f7a52}
    .notice.error{background:#fde8e8;color:#943535}
    .warning{padding:10px 12px;border-radius:12px;background:#fff0e1;color:#8a541e;margin-top:10px}
    input,textarea{width:100%;box-sizing:border-box;border:1px solid #dbe4db;border-radius:12px;padding:10px;background:#f8fbf8;color:#162118}
    textarea{min-height:90px}
    button{border:0;border-radius:10px;padding:9px 12px;background:#1f7a52;color:#fff;font-weight:700;cursor:pointer}
    button.secondary{background:#2759c4}
    button.ghost{background:#3e4d40}
    button.danger{background:#b42318}
    .token-actions{display:flex;gap:8px;flex-wrap:wrap;margin-top:10px}
    table{width:100%;border-collapse:collapse}
    th,td{padding:10px;border-bottom:1px solid #dbe4db;text-align:left;vertical-align:top;font-size:14px}
    th{color:#5f6e61}
    @media (max-width:860px){.hero,.row{flex-direction:column}table,thead,tbody,tr,th,td{display:block}thead{display:none}}
  </style>
  <script>
    function setButtonLabel(button, label){
      if(!button) return;
      const original = button.dataset.originalLabel || button.textContent;
      button.dataset.originalLabel = original;
      button.textContent = label;
      window.clearTimeout(button._resetTimer);
      button._resetTimer = window.setTimeout(function(){
        button.textContent = button.dataset.originalLabel || original;
      }, 1800);
    }
    function toggleSecret(id, button){
      const box = document.getElementById(id);
      if(!box) return;
      const visible = box.dataset.visible === 'true';
      if(visible){
        box.value = box.dataset.masked || '';
        box.dataset.visible = 'false';
        if(button) button.textContent = '显示详细 Token';
        return;
      }
      box.value = box.dataset.secret || '';
      box.dataset.visible = 'true';
      if(button) button.textContent = '隐藏 Token';
    }
    async function copyText(id, button){
      const box = document.getElementById(id);
      if(!box) return;
      button = button || (document.activeElement && document.activeElement.tagName === 'BUTTON' ? document.activeElement : null);
      const text = box.dataset.secret || box.value || '';
      if(!text) return;
      let copied = false;
      if(navigator.clipboard && window.isSecureContext){
        try{
          await navigator.clipboard.writeText(text);
          copied = true;
        }catch(err){}
      }
      if(!copied){
        const helper = document.createElement('textarea');
        helper.value = text;
        helper.setAttribute('readonly', 'readonly');
        helper.style.position = 'fixed';
        helper.style.opacity = '0';
        helper.style.pointerEvents = 'none';
        document.body.appendChild(helper);
        helper.focus();
        helper.select();
        helper.setSelectionRange(0, helper.value.length);
        try{
          copied = document.execCommand('copy');
        }catch(err){}
        document.body.removeChild(helper);
      }
      if(copied){
        setButtonLabel(button, '已复制');
        return;
      }
      box.value = text;
      box.select();
      box.setSelectionRange(0, box.value.length);
      box.focus();
      setButtonLabel(button, '复制失败，请按 Ctrl+C');
    }
    window.addEventListener('load', function(){if(window.location.search){window.history.replaceState({}, document.title, window.location.pathname);}});
  </script>
</head>
<body>
  <div class="wrap">
    <div class="hero card">
      <div>
        <h1 style="margin:0 0 8px">{{.Version}} Console</h1>
        <div class="muted">0.1.2 起服务端改为每节点独立凭据，并支持 TCP / UDP 以及映射状态重启恢复。页面每 5 秒自动刷新一次。</div>
        <div class="muted" style="margin-top:8px">最近刷新: {{.RefreshedAt}}</div>
      </div>
      <form method="post" action="/logout"><button type="submit" class="ghost">退出登录</button></form>
    </div>

    {{if .Message}}<div class="notice">{{.Message}}</div>{{end}}
    {{if .Error}}<div class="notice error">{{.Error}}</div>{{end}}

    <div class="card">
      <h2 style="margin-top:0">运行概览</h2>
      <div class="grid">
        <div class="metric"><div class="muted">客户端节点</div><div class="value">{{.Summary.TotalNodes}}</div><div class="muted">在线 {{.Summary.OnlineNodes}} / 离线 {{.Summary.OfflineNodes}}</div></div>
        <div class="metric"><div class="muted">已发布端口</div><div class="value">{{.Summary.PublishedTunnels}}</div><div class="muted">可用 {{.Summary.AvailablePorts}} | 使用率 {{.Summary.PortUsage}}</div></div>
        <div class="metric"><div class="muted">活跃连接</div><div class="value">{{.Summary.ActiveConnections}}</div><div class="muted">累计连接 {{.Summary.TotalConnections}}</div></div>
        <div class="metric"><div class="muted">入口流量</div><div class="value">{{.Summary.IngressBytes}}</div><div class="muted">公网访问进入</div></div>
        <div class="metric"><div class="muted">出口流量</div><div class="value">{{.Summary.EgressBytes}}</div><div class="muted">客户端回传</div></div>
      </div>
    </div>

    <div class="card">
      <h2 style="margin-top:0">服务信息</h2>
      <div class="grid">
        <div class="metric">
          <div class="muted">控制入口</div>
          <div class="mono">{{.ControlAddress}}</div>
          <div class="muted" style="margin-top:8px">控制台地址</div>
          <div><a href="{{.WebURL}}" target="_blank" rel="noreferrer">{{.WebURL}}</a></div>
        </div>
        <div class="metric">
          <div class="muted">公网端口池</div>
          <div class="mono">{{.PortRange}}</div>
          <div class="muted" style="margin-top:8px">状态文件</div>
          <div class="mono">{{.StateFile}}</div>
        </div>
      </div>
    </div>

    <div class="card">
      <h2 style="margin-top:0">节点凭据签发</h2>
      <div class="muted">为每个节点单独签发 bootstrap token。相同节点再次签发会轮换凭据，并使旧凭据失效。</div>
      <form method="post" action="/nodes/issue" style="margin-top:12px">
        <div class="grid">
          <div>
            <div class="muted" style="margin-bottom:6px">节点名称</div>
            <input type="text" name="node_name" placeholder="例如：cust-win-01">
          </div>
          <div style="align-self:end">
            <button type="submit">签发 / 重置节点凭据</button>
          </div>
        </div>
      </form>

      {{if .IssuedConnectToken}}
        <div class="endpoint" style="margin-top:14px">
          <div><strong>刚签发的节点：</strong>{{.IssuedNodeName}}</div>
          <div class="muted" style="margin-top:6px">把下面这串 token 发给对应客户端即可。</div>
          <textarea id="issued-connect-token" readonly class="mono" spellcheck="false" data-secret="{{.IssuedConnectToken}}" data-masked="{{.IssuedTokenMasked}}" data-visible="false" style="margin-top:10px">{{.IssuedTokenMasked}}</textarea>
          <div class="token-actions">
            <button type="button" onclick="toggleSecret('issued-connect-token', this)">显示详细 Token</button>
          </div>
          <div style="margin-top:10px"><button type="button" class="secondary" onclick="copyText('issued-connect-token', this)">复制节点 Token</button></div>
        </div>
      {{end}}
    </div>

    <div class="card">
      <h2 style="margin-top:0">客户端节点</h2>
      {{if .Nodes}}
        <div class="node-grid">
          {{range .Nodes}}
            <div class="metric">
              <div class="row">
                <div>
                  <h3 style="margin:0 0 8px">{{.Name}}</h3>
                  <div class="muted">
                    主机名: {{.Hostname}}<br>
                    平台: {{.Platform}}<br>
                    控制连接来源: {{.Remote}}<br>
                    凭据签发: {{.CredentialIssuedAt}}<br>
                    连接开始: {{.ConnectedSince}}<br>
                    最近活动: {{.LastSeen}}
                  </div>
                </div>
                <span class="status {{.StatusClass}}">{{.StatusText}}</span>
              </div>

              {{if .ProvisionError}}<div class="warning">端口分配告警: {{.ProvisionError}}</div>{{end}}

              <div class="grid" style="margin-top:12px">
                <div class="endpoint"><div class="muted">预设 / 已发布</div><div class="value" style="font-size:20px">{{.PresetCount}} / {{.PublishedCount}}</div></div>
                <div class="endpoint"><div class="muted">活跃 / 累计连接</div><div class="value" style="font-size:20px">{{.ActiveConnections}} / {{.TotalConnections}}</div></div>
                <div class="endpoint"><div class="muted">入口流量</div><div class="value" style="font-size:20px">{{.IngressBytes}}</div></div>
                <div class="endpoint"><div class="muted">出口流量</div><div class="value" style="font-size:20px">{{.EgressBytes}}</div></div>
              </div>

              <div class="actions">
                <form method="post" action="/nodes/resync">
                  <input type="hidden" name="node_name" value="{{.Name}}">
                  <button type="submit" class="secondary">重试端口同步</button>
                </form>
                <form method="post" action="/nodes/issue">
                  <input type="hidden" name="node_name" value="{{.Name}}">
                  <button type="submit">重置节点凭据</button>
                </form>
                <form method="post" action="/nodes/delete" onsubmit="return confirm('Delete this node and stop all published services?');">
                  <input type="hidden" name="node_name" value="{{.Name}}">
                  <button type="submit" class="danger">删除节点并停止服务</button>
                </form>
              </div>

              {{range .Endpoints}}
                <div class="endpoint">
                  <div class="row">
                    <div>
                      <div><strong>{{.PresetName}}</strong> <span class="muted">({{.Protocol}})</span></div>
                      <div class="muted">
                        本地地址: <span class="mono">{{.LocalAddr}}</span><br>
                        公网入口: <span class="mono">{{.PublicTarget}}</span><br>
                        建立时间: {{.CreatedAt}}<br>
                        最近活动: {{.LastActivity}}
                      </div>
                    </div>
                    <span class="status {{.StatusClass}}">{{.StatusText}}</span>
                  </div>
                  <div class="grid" style="margin-top:10px">
                    <div><span class="muted">活跃 / 等待附加</span><div>{{.ActiveConnections}} / {{.PendingConnections}}</div></div>
                    <div><span class="muted">累计连接</span><div>{{.TotalConnections}}</div></div>
                    <div><span class="muted">入口流量</span><div>{{.IngressBytes}}</div></div>
                    <div><span class="muted">出口流量</span><div>{{.EgressBytes}}</div></div>
                  </div>
                  <div class="muted" style="margin-top:8px">连接提示: <span class="mono">{{.CommandHint}}</span></div>
                  <div class="muted">最近错误: {{.LastError}}</div>
                  {{if .Assigned}}
                    <form method="post" action="/tunnels/reassign" style="margin-top:10px">
                      <input type="hidden" name="tunnel_id" value="{{.TunnelID}}">
                      <button type="submit">重新分配随机端口</button>
                    </form>
                  {{end}}
                </div>
              {{end}}
            </div>
          {{end}}
        </div>
      {{else}}
        <div class="muted">还没有已签发或已接入的节点。先在上面签发一个节点凭据，再把 token 发给客户端。</div>
      {{end}}
    </div>

    <div class="card">
      <h2 style="margin-top:0">端口发布总览</h2>
      {{if .Tunnels}}
        <table>
          <thead>
            <tr>
              <th>公网端口</th>
              <th>协议</th>
              <th>节点</th>
              <th>预设</th>
              <th>本地地址</th>
              <th>状态</th>
              <th>活跃/等待</th>
              <th>累计连接</th>
              <th>入口流量</th>
              <th>出口流量</th>
              <th>最近活动</th>
            </tr>
          </thead>
          <tbody>
            {{range .Tunnels}}
              <tr>
                <td class="mono">{{.PublicTarget}}</td>
                <td>{{.Protocol}}</td>
                <td>{{.NodeName}}</td>
                <td>{{.PresetName}}</td>
                <td class="mono">{{.LocalAddr}}</td>
                <td><span class="status {{.StatusClass}}">{{.StatusText}}</span></td>
                <td>{{.ActiveConnections}} / {{.PendingConnections}}</td>
                <td>{{.TotalConnections}}</td>
                <td>{{.IngressBytes}}</td>
                <td>{{.EgressBytes}}</td>
                <td>{{.LastActivity}}</td>
              </tr>
            {{end}}
          </tbody>
        </table>
      {{else}}
        <div class="muted">当前还没有已发布的公网端口。</div>
      {{end}}
    </div>
  </div>
</body>
</html>
`))

var loginTemplate = template.Must(template.New("login").Parse(`
<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <title>1cat Tunnel Login</title>
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <style>
    body{margin:0;font-family:"Segoe UI","PingFang SC","Microsoft YaHei",sans-serif;min-height:100vh;display:grid;place-items:center;background:#eef4ee}
    .box{width:min(92vw,420px);background:#fff;padding:26px;border-radius:18px;border:1px solid #dbe4db;box-shadow:0 12px 30px rgba(0,0,0,.05)}
    input{width:100%;box-sizing:border-box;margin-bottom:12px;padding:12px 14px;border-radius:12px;border:1px solid #dbe4db;background:#fafcf9}
    button{width:100%;border:0;border-radius:12px;padding:12px 14px;background:#1f7a52;color:#fff;font-weight:700}
    .error{margin-bottom:12px;padding:10px 12px;border-radius:12px;background:#fde8e8;color:#943535}
  </style>
</head>
<body>
  <form class="box" method="post" action="/login">
    <h1 style="margin-top:0">登录控制台</h1>
    <p style="color:#5f6e61;line-height:1.6">建议把控制台放在管理网络里，或放在 HTTPS 反向代理之后使用。</p>
    {{if .Error}}<div class="error">{{.Error}}</div>{{end}}
    <input type="text" name="username" placeholder="用户名" autocomplete="username">
    <input type="password" name="password" placeholder="密码" autocomplete="current-password">
    <button type="submit">进入控制台</button>
  </form>
</body>
</html>
`))

func (s *Server) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/login", s.handleLogin)
	mux.HandleFunc("/logout", s.handleLogout)
	mux.Handle("/", s.requireAuth(http.HandlerFunc(s.handleDashboard)))
	mux.Handle("/nodes/issue", s.requireAuth(http.HandlerFunc(s.handleIssueNode)))
	mux.Handle("/nodes/delete", s.requireAuth(http.HandlerFunc(s.handleDeleteNode)))
	mux.Handle("/nodes/resync", s.requireAuth(http.HandlerFunc(s.handleResyncNode)))
	mux.Handle("/tunnels/reassign", s.requireAuth(http.HandlerFunc(s.handleReassignTunnel)))
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		_ = loginTemplate.Execute(w, map[string]string{
			"Error": r.URL.Query().Get("error"),
		})
	case http.MethodPost:
		username := strings.TrimSpace(r.FormValue("username"))
		password := r.FormValue("password")
		if subtle.ConstantTimeCompare([]byte(username), []byte(s.cfg.AdminUsername)) != 1 ||
			subtle.ConstantTimeCompare([]byte(password), []byte(s.cfg.AdminPassword)) != 1 {
			http.Redirect(w, r, "/login?error="+url.QueryEscape("用户名或密码不正确"), http.StatusSeeOther)
			return
		}

		token, err := generateSessionToken()
		if err != nil {
			http.Redirect(w, r, "/login?error="+url.QueryEscape("生成会话失败"), http.StatusSeeOther)
			return
		}

		s.sessionMu.Lock()
		s.sessions[token] = &webSession{
			ExpireAt: time.Now().Add(24 * time.Hour),
		}
		s.sessionMu.Unlock()

		http.SetCookie(w, &http.Cookie{
			Name:     sessionCookieName,
			Value:    token,
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})
		http.Redirect(w, r, "/", http.StatusSeeOther)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		s.sessionMu.Lock()
		delete(s.sessions, cookie.Value)
		s.sessionMu.Unlock()
	}

	http.SetCookie(w, &http.Cookie{
		Name:   sessionCookieName,
		Value:  "",
		Path:   "/",
		MaxAge: -1,
	})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil || cookie.Value == "" {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}

		s.sessionMu.Lock()
		session, ok := s.sessions[cookie.Value]
		if ok && (session == nil || time.Now().After(session.ExpireAt)) {
			delete(s.sessions, cookie.Value)
			ok = false
		}
		s.sessionMu.Unlock()

		if !ok {
			http.Redirect(w, r, "/login?error="+url.QueryEscape("会话已过期，请重新登录"), http.StatusSeeOther)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	flash := dashboardFlash{
		Message: r.URL.Query().Get("message"),
		Error:   r.URL.Query().Get("error"),
	}
	if cookie, err := r.Cookie(sessionCookieName); err == nil && cookie.Value != "" {
		flash = mergeFlash(flash, s.consumeSessionFlash(cookie.Value))
		issuedNodeName, issuedConnectToken := s.sessionIssuedCredential(cookie.Value)
		flash = mergeFlash(flash, dashboardFlash{
			IssuedNodeName:     issuedNodeName,
			IssuedConnectToken: issuedConnectToken,
		})
	}

	s.renderDashboard(w, r, flash)
}

func (s *Server) renderDashboard(w http.ResponseWriter, r *http.Request, flash dashboardFlash) {
	publicHost := s.resolvedPublicHost()
	controlAddress, err := s.publicControlAddress()
	if err != nil {
		controlAddress = s.cfg.ControlListenAddr
	}

	allNodes := s.listNodes()
	tunnelList := s.listTunnels()
	tunnelsByNode := make(map[string][]*Tunnel)
	for _, tunnel := range tunnelList {
		tunnelsByNode[tunnel.nodeName] = append(tunnelsByNode[tunnel.nodeName], tunnel)
	}

	nodes := make([]dashboardNode, 0, len(allNodes))
	var (
		onlineNodes       int
		activeConnections int64
		totalConnections  uint64
		totalIngress      uint64
		totalEgress       uint64
	)

	for _, node := range allNodes {
		node.mu.RLock()
		connected := node.connected
		hostname := node.hostname
		platform := node.platform
		remote := node.remote
		connectedAt := node.connectedAt
		lastSeen := node.lastSeen
		credentialIssuedAt := node.credentialIssuedAt
		provisionError := node.lastProvisionError
		presets := make([]commonPresetView, 0, len(node.presets))
		for _, preset := range node.presets {
			presets = append(presets, commonPresetView{
				Name:      preset.Name,
				LocalAddr: preset.LocalAddr,
				Protocol:  common.NormalizeProtocol(preset.Protocol),
			})
		}
		node.mu.RUnlock()

		if connected {
			onlineNodes++
		}

		sort.Slice(presets, func(i, j int) bool {
			return presets[i].Name < presets[j].Name
		})

		endpoints := make([]dashboardEndpoint, 0)
		assignedPresets := make(map[string]struct{})
		var nodeActive int64
		var nodeTotal uint64
		var nodeIngress uint64
		var nodeEgress uint64

		for _, tunnel := range tunnelsByNode[node.name] {
			lastError, lastErrorAt := tunnel.lastErrorSnapshot()
			active := tunnel.activeConnections.Load()
			pending := tunnel.pendingCount()
			total := tunnel.totalConnections.Load()
			ingress := tunnel.ingressBytes.Load()
			egress := tunnel.egressBytes.Load()

			nodeActive += active
			nodeTotal += total
			nodeIngress += ingress
			nodeEgress += egress

			activeConnections += active
			totalConnections += total
			totalIngress += ingress
			totalEgress += egress

			statusText, statusClass := endpointStatus(connected, active, pending, lastError)
			endpoints = append(endpoints, dashboardEndpoint{
				TunnelID:           tunnel.id,
				PresetName:         tunnel.presetName,
				Protocol:           strings.ToUpper(tunnel.protocol),
				LocalAddr:          node.presetLocalAddr(tunnel.presetName),
				PublicTarget:       fmt.Sprintf("%s:%d", publicHost, tunnel.publicPort),
				StatusText:         statusText,
				StatusClass:        statusClass,
				CommandHint:        buildCommandHint(publicHost, tunnel.protocol, tunnel.presetName, tunnel.publicPort),
				CreatedAt:          formatTime(tunnel.createdAt),
				LastActivity:       formatTime(tunnel.lastActivity()),
				LastError:          formatError(lastError, lastErrorAt),
				ActiveConnections:  active,
				PendingConnections: pending,
				TotalConnections:   total,
				IngressBytes:       formatBytes(ingress),
				EgressBytes:        formatBytes(egress),
				Assigned:           true,
			})
			assignedPresets[tunnel.presetName] = struct{}{}
		}

		for _, preset := range presets {
			if _, ok := assignedPresets[preset.Name]; ok {
				continue
			}
			endpoints = append(endpoints, dashboardEndpoint{
				PresetName:         preset.Name,
				Protocol:           strings.ToUpper(common.NormalizeProtocol(preset.Protocol)),
				LocalAddr:          preset.LocalAddr,
				PublicTarget:       "未分配",
				StatusText:         "未分配端口",
				StatusClass:        "warn",
				CommandHint:        "请点击“重试端口同步”重新申请端口",
				CreatedAt:          "-",
				LastActivity:       "-",
				LastError:          fallbackText(provisionError, "尚未建立公网入口"),
				ActiveConnections:  0,
				PendingConnections: 0,
				TotalConnections:   0,
				IngressBytes:       formatBytes(0),
				EgressBytes:        formatBytes(0),
				Assigned:           false,
			})
		}

		sort.Slice(endpoints, func(i, j int) bool {
			if endpoints[i].Assigned != endpoints[j].Assigned {
				return endpoints[i].Assigned
			}
			if endpoints[i].Protocol == endpoints[j].Protocol {
				return endpoints[i].PresetName < endpoints[j].PresetName
			}
			return endpoints[i].Protocol < endpoints[j].Protocol
		})

		statusText := "离线"
		statusClass := "offline"
		if connected {
			statusText = "在线"
			statusClass = ""
		}
		if provisionError != "" {
			statusText = "告警"
			statusClass = "warn"
		}

		nodes = append(nodes, dashboardNode{
			Name:               node.name,
			Hostname:           fallbackText(hostname, "-"),
			Platform:           fallbackText(platform, "-"),
			Remote:             fallbackText(remote, "-"),
			StatusText:         statusText,
			StatusClass:        statusClass,
			ConnectedSince:     formatTime(connectedAt),
			LastSeen:           formatTime(lastSeen),
			CredentialIssuedAt: formatTime(credentialIssuedAt),
			ProvisionError:     provisionError,
			PresetCount:        len(presets),
			PublishedCount:     len(tunnelsByNode[node.name]),
			ActiveConnections:  nodeActive,
			TotalConnections:   nodeTotal,
			IngressBytes:       formatBytes(nodeIngress),
			EgressBytes:        formatBytes(nodeEgress),
			Endpoints:          endpoints,
		})
	}

	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].Name < nodes[j].Name
	})

	tunnels := make([]dashboardTunnel, 0, len(tunnelList))
	for _, tunnel := range tunnelList {
		node := s.getNode(tunnel.nodeName)
		connected := false
		localAddr := ""
		if node != nil {
			node.mu.RLock()
			connected = node.connected
			node.mu.RUnlock()
			localAddr = node.presetLocalAddr(tunnel.presetName)
		}

		lastError, lastErrorAt := tunnel.lastErrorSnapshot()
		active := tunnel.activeConnections.Load()
		pending := tunnel.pendingCount()
		total := tunnel.totalConnections.Load()
		ingress := tunnel.ingressBytes.Load()
		egress := tunnel.egressBytes.Load()
		statusText, statusClass := endpointStatus(connected, active, pending, lastError)

		tunnels = append(tunnels, dashboardTunnel{
			ID:                 tunnel.id,
			NodeName:           tunnel.nodeName,
			PresetName:         tunnel.presetName,
			Protocol:           strings.ToUpper(tunnel.protocol),
			LocalAddr:          fallbackText(localAddr, "-"),
			PublicTarget:       fmt.Sprintf("%s:%d", publicHost, tunnel.publicPort),
			StatusText:         statusText,
			StatusClass:        statusClass,
			CommandHint:        buildCommandHint(publicHost, tunnel.protocol, tunnel.presetName, tunnel.publicPort),
			CreatedAt:          formatTime(tunnel.createdAt),
			LastActivity:       formatTime(tunnel.lastActivity()),
			LastError:          formatError(lastError, lastErrorAt),
			ActiveConnections:  active,
			PendingConnections: pending,
			TotalConnections:   total,
			IngressBytes:       formatBytes(ingress),
			EgressBytes:        formatBytes(egress),
		})
	}

	totalPortCount := (s.cfg.AutoPortEnd - s.cfg.AutoPortStart) + 1
	if totalPortCount < 0 {
		totalPortCount = 0
	}
	availablePorts := totalPortCount - len(tunnelList)
	if availablePorts < 0 {
		availablePorts = 0
	}

	data := dashboardData{
		Version:            common.Version,
		RefreshedAt:        formatTime(time.Now()),
		Message:            flash.Message,
		Error:              flash.Error,
		IssuedNodeName:     flash.IssuedNodeName,
		IssuedConnectToken: flash.IssuedConnectToken,
		IssuedTokenMasked:  maskSecret(flash.IssuedConnectToken),
		ControlAddress:     controlAddress,
		WebURL:             s.publicWebURL(),
		PortRange:          fmt.Sprintf("%d-%d", s.cfg.AutoPortStart, s.cfg.AutoPortEnd),
		StateFile:          s.cfg.StateFile,
		Summary: dashboardSummary{
			TotalNodes:        len(nodes),
			OnlineNodes:       onlineNodes,
			OfflineNodes:      len(nodes) - onlineNodes,
			PublishedTunnels:  len(tunnelList),
			AvailablePorts:    availablePorts,
			PortUsage:         usagePercent(len(tunnelList), totalPortCount),
			ActiveConnections: activeConnections,
			TotalConnections:  totalConnections,
			IngressBytes:      formatBytes(totalIngress),
			EgressBytes:       formatBytes(totalEgress),
		},
		Nodes:   nodes,
		Tunnels: tunnels,
	}
	_ = dashboardTemplate.Execute(w, data)
}

func (s *Server) handleIssueNode(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	nodeName := strings.TrimSpace(r.FormValue("node_name"))
	node, token, created, err := s.issueNodeCredential(nodeName)
	if err != nil {
		s.writeSessionFlashFromRequest(r, dashboardFlash{Error: err.Error()})
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	message := "已重置节点凭据: " + node.name
	if created {
		message = "已创建节点并签发独立凭据: " + node.name
	}
	s.writeSessionIssuedCredentialFromRequest(r, node.name, token)
	s.writeSessionFlashFromRequest(r, dashboardFlash{
		Message: message,
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleDeleteNode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	nodeName := strings.TrimSpace(r.FormValue("node_name"))
	removedTunnels, wasConnected, err := s.deleteNode(nodeName)
	if err != nil {
		s.writeSessionFlashFromRequest(r, dashboardFlash{Error: err.Error()})
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	s.clearSessionIssuedCredentialFromRequest(r, nodeName)

	message := fmt.Sprintf("已删除节点 %s，并停止 %d 个发布服务", nodeName, removedTunnels)
	if wasConnected {
		message += "，已断开在线客户端"
	}
	s.writeSessionFlashFromRequest(r, dashboardFlash{
		Message: message,
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleResyncNode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	nodeName := strings.TrimSpace(r.FormValue("node_name"))
	if nodeName == "" {
		http.Redirect(w, r, "/?error="+url.QueryEscape("缺少 node_name"), http.StatusSeeOther)
		return
	}
	if s.getNode(nodeName) == nil {
		http.Redirect(w, r, "/?error="+url.QueryEscape("节点不存在"), http.StatusSeeOther)
		return
	}

	s.syncNodeTunnels(nodeName)
	http.Redirect(w, r, "/?message="+url.QueryEscape("已重新同步节点端口: "+nodeName), http.StatusSeeOther)
}

func (s *Server) handleReassignTunnel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	tunnelID := strings.TrimSpace(r.FormValue("tunnel_id"))
	if tunnelID == "" {
		http.Redirect(w, r, "/?error="+url.QueryEscape("缺少 tunnel_id"), http.StatusSeeOther)
		return
	}

	tunnel, err := s.reassignTunnel(tunnelID)
	if err != nil {
		http.Redirect(w, r, "/?error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}

	message := fmt.Sprintf("已重新分配端口: %s / %s / %s -> %d", tunnel.nodeName, tunnel.presetName, strings.ToUpper(tunnel.protocol), tunnel.publicPort)
	http.Redirect(w, r, "/?message="+url.QueryEscape(message), http.StatusSeeOther)
}

func generateSessionToken() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func endpointStatus(connected bool, active int64, pending int, lastError string) (string, string) {
	switch {
	case !connected:
		return "客户端离线", "offline"
	case active > 0:
		return "转发中", ""
	case pending > 0:
		return "等待附加", "warn"
	case strings.TrimSpace(lastError) != "":
		return "最近有错误", "warn"
	default:
		return "已就绪", ""
	}
}

func usagePercent(used, total int) string {
	if total <= 0 {
		return "0%"
	}
	return fmt.Sprintf("%.1f%%", (float64(used)/float64(total))*100)
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Local().Format("2006-01-02 15:04:05")
}

func formatError(message string, at time.Time) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return "-"
	}
	if at.IsZero() {
		return message
	}
	return message + " (" + formatTime(at) + ")"
}

func fallbackText(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func formatBytes(bytes uint64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}

	value := float64(bytes)
	suffixes := []string{"KB", "MB", "GB", "TB"}
	suffix := suffixes[0]
	for _, current := range suffixes {
		value = value / unit
		suffix = current
		if value < unit || current == suffixes[len(suffixes)-1] {
			break
		}
	}
	return fmt.Sprintf("%.2f %s", value, suffix)
}

func maskSecret(secret string) string {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return ""
	}
	return strings.Repeat("*", len(secret))
}

func mergeFlash(base, overlay dashboardFlash) dashboardFlash {
	if strings.TrimSpace(overlay.Message) != "" {
		base.Message = overlay.Message
	}
	if strings.TrimSpace(overlay.Error) != "" {
		base.Error = overlay.Error
	}
	if strings.TrimSpace(overlay.IssuedNodeName) != "" {
		base.IssuedNodeName = overlay.IssuedNodeName
	}
	if strings.TrimSpace(overlay.IssuedConnectToken) != "" {
		base.IssuedConnectToken = overlay.IssuedConnectToken
	}
	return base
}

func (s *Server) writeSessionFlashFromRequest(r *http.Request, flash dashboardFlash) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		return
	}

	s.sessionMu.Lock()
	defer s.sessionMu.Unlock()

	session := s.sessions[cookie.Value]
	if session == nil {
		return
	}
	session.Flash = flash
}

func (s *Server) writeSessionIssuedCredentialFromRequest(r *http.Request, nodeName, connectToken string) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		return
	}

	s.sessionMu.Lock()
	defer s.sessionMu.Unlock()

	session := s.sessions[cookie.Value]
	if session == nil {
		return
	}
	session.LastIssuedNodeName = strings.TrimSpace(nodeName)
	session.LastIssuedConnectToken = strings.TrimSpace(connectToken)
}

func (s *Server) clearSessionIssuedCredentialFromRequest(r *http.Request, nodeName string) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		return
	}

	nodeName = strings.TrimSpace(nodeName)

	s.sessionMu.Lock()
	defer s.sessionMu.Unlock()

	session := s.sessions[cookie.Value]
	if session == nil {
		return
	}
	if nodeName != "" && session.LastIssuedNodeName != nodeName {
		return
	}
	session.LastIssuedNodeName = ""
	session.LastIssuedConnectToken = ""
}

func (s *Server) consumeSessionFlash(token string) dashboardFlash {
	s.sessionMu.Lock()
	defer s.sessionMu.Unlock()

	session := s.sessions[token]
	if session == nil {
		return dashboardFlash{}
	}

	flash := session.Flash
	session.Flash = dashboardFlash{}
	return flash
}

func (s *Server) sessionIssuedCredential(token string) (string, string) {
	s.sessionMu.Lock()
	defer s.sessionMu.Unlock()

	session := s.sessions[token]
	if session == nil {
		return "", ""
	}
	return session.LastIssuedNodeName, session.LastIssuedConnectToken
}
