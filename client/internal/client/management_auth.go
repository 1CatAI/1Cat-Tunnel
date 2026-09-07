package client

import (
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

//go:embed login.js
var clientLoginJavaScript string

type managementAccess struct {
	Token string `json:"token"`
}

func (c *Client) initializeManagementAccess() error {
	key, err := newClientCSRFToken()
	if err != nil {
		return err
	}
	cfg := c.currentConfig()
	if cfg.configPath != "" {
		protected, err := protectManagementKey(key)
		if err != nil {
			return err
		}
		data, err := json.Marshal(managementAccess{Token: protected})
		if err != nil {
			return err
		}
		if err := writeSecureConfig(cfg.configPath+".web-auth.json", data); err != nil {
			return err
		}
	}
	c.webAccessToken = key
	return nil
}

func (c *Client) authenticateManagement(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The block-IP endpoint retains its separate server-admin authentication.
		public := r.URL.Path == "/" || r.URL.Path == "/health" || r.URL.Path == "/assets/login.js" ||
			r.URL.Path == "/assets/management.js" || r.URL.Path == "/api/block-ip"
		if !public {
			key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") || len(c.webAccessToken) != 64 || subtle.ConstantTimeCompare([]byte(key), []byte(c.webAccessToken)) != 1 {
				writeClientJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "请使用本机账户的管理登录链接"})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (c *Client) handleManagementLogin(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>1CatTunnel 管理登录</title></head><body><main><h1>1CatTunnel 本机管理</h1><p id="login-message">请在运行客户端的本机账户下执行 1cattunnel panel，打开生成的专用登录链接。</p><p>登录链接相当于本机管理密码，请勿分享。客户端重启后自动失效。</p></main><script src="/assets/login.js" defer></script></body></html>`)
}

func managementKey(cfg Config) (string, error) {
	data, err := readPrivateFile(cfg.configPath + ".web-auth.json")
	if err != nil {
		return "", fmt.Errorf("请以运行客户端的同一个系统账户获取管理链接: %w", err)
	}
	var access managementAccess
	if err := json.Unmarshal(data, &access); err != nil {
		return "", err
	}
	key, err := restoreManagementKey(access.Token)
	if err != nil {
		return "", err
	}
	if len(key) != 64 {
		return "", fmt.Errorf("管理凭据无效，请重启客户端")
	}
	return key, nil
}

// QueryManagement uses no proxy and sends owner authentication only to loopback.
func QueryManagement(cfg Config, endpoint string, authenticated bool) ([]byte, error) {
	if clientWebDisabled(cfg.WebListenAddr) {
		return nil, fmt.Errorf("本机管理页面已禁用")
	}
	if err := validateClientWebListenAddr(cfg.WebListenAddr); err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodGet, "http://"+cfg.WebListenAddr+endpoint, nil)
	if err != nil {
		return nil, err
	}
	if authenticated {
		key, err := managementKey(cfg)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+key)
	}
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("管理接口返回 %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

func ManagementURL(cfg Config) (string, error) {
	if _, err := QueryManagement(cfg, "/api/status", true); err != nil {
		return "", err
	}
	key, err := managementKey(cfg)
	if err != nil {
		return "", err
	}
	return "http://" + cfg.WebListenAddr + "/#access=" + key, nil
}

// InitializeManagedConfig never connects to a server. Installers must invoke
// this as the service owner, not as root on a customer's writable paths.
func InitializeManagedConfig(path string) error {
	cfg, err := LoadManagedConfig(path)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	return SaveConfig(path, cfg)
}
