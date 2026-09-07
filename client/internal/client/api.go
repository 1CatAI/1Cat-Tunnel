package client

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"time"
)

type blockIPLocalRequest struct {
	IP            string `json:"ip"`
	Reason        string `json:"reason"`
	AdminUsername string `json:"admin_username"`
	AdminPassword string `json:"admin_password"`
}

func (c *Client) startLocalWeb(ctx context.Context) error {
	addr := strings.TrimSpace(c.currentConfig().WebListenAddr)
	if clientWebDisabled(addr) {
		return nil
	}
	if err := validateClientWebListenAddr(addr); err != nil {
		return fmt.Errorf("start local management panel: %w", err)
	}
	csrfToken, err := newClientCSRFToken()
	if err != nil {
		return fmt.Errorf("generate local management security token: %w", err)
	}
	c.webCSRFToken = csrfToken

	mux := http.NewServeMux()
	mux.HandleFunc("/", c.handleManagementLogin)
	mux.HandleFunc("/api/view", c.handleManagementDashboard)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		writeClientJSON(w, http.StatusOK, map[string]any{
			"ok":      true,
			"version": clientVersion(),
		})
	})
	mux.HandleFunc("/api/status", c.handleManagementStatus)
	mux.HandleFunc("/api/config", c.handleManagementConfig)
	mux.HandleFunc("/api/servers", c.handleManagementServers)
	mux.HandleFunc("/assets/management.js", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		_, _ = w.Write([]byte(clientManagementJavaScript))
	})
	mux.HandleFunc("/api/reconnect", c.handleManagementReconnect)
	mux.HandleFunc("/api/block-ip", c.handleLocalBlockIP)
	mux.HandleFunc("/assets/login.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		_, _ = w.Write([]byte(clientLoginJavaScript))
	})

	server := &http.Server{
		Addr:              addr,
		Handler:           localManagementOnly(c.authenticateManagement(mux)),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("本机管理端口 %s 无法监听（客户端可能已经在运行）: %w", addr, err)
	}
	// Bind first: a second launch must never invalidate the running owner's key.
	if err := c.initializeManagementAccess(); err != nil {
		_ = listener.Close()
		return fmt.Errorf("initialize management authentication: %w", err)
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	go func() {
		log.Printf("local management panel listening on http://%s/", addr)
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Printf("local management panel disabled: %v", err)
		}
	}()
	return nil
}

func (c *Client) handleLocalBlockIP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeClientJSON(w, http.StatusMethodNotAllowed, map[string]any{
			"ok":    false,
			"error": "method not allowed",
		})
		return
	}

	req, username, password, err := readLocalBlockIPRequest(w, r)
	if err != nil {
		writeClientJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": err.Error(),
		})
		return
	}
	if strings.TrimSpace(username) == "" || strings.TrimSpace(password) == "" {
		writeClientJSON(w, http.StatusUnauthorized, map[string]any{
			"ok":    false,
			"error": "admin credential is required",
		})
		return
	}

	ip := strings.TrimSpace(req.IP)
	if parsed := net.ParseIP(ip); parsed == nil {
		writeClientJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": "invalid ip",
		})
		return
	}

	sess := c.currentSession()
	if sess == nil {
		writeClientJSON(w, http.StatusServiceUnavailable, map[string]any{
			"ok":    false,
			"error": "client is not connected to the server",
		})
		return
	}

	timeoutCtx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	resp, err := sess.submitBlockIP(timeoutCtx, username, password, ip, req.Reason)
	if err != nil {
		writeClientJSON(w, http.StatusBadGateway, map[string]any{
			"ok":    false,
			"error": err.Error(),
		})
		return
	}

	writeClientJSON(w, http.StatusOK, map[string]any{
		"ok":             true,
		"ip":             resp.IP,
		"already_exists": resp.AlreadyExists,
	})
}

func readLocalBlockIPRequest(w http.ResponseWriter, r *http.Request) (blockIPLocalRequest, string, string, error) {
	var req blockIPLocalRequest
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	username, password, ok := r.BasicAuth()

	if strings.Contains(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			return req, "", "", err
		}
	} else {
		if err := r.ParseForm(); err != nil {
			return req, "", "", err
		}
		req.IP = r.FormValue("ip")
		req.Reason = r.FormValue("reason")
		req.AdminUsername = r.FormValue("admin_username")
		req.AdminPassword = r.FormValue("admin_password")
	}

	if !ok {
		username = req.AdminUsername
		password = req.AdminPassword
	}
	return req, username, password, nil
}

func blockIPAPIDisabled(addr string) bool {
	return clientWebDisabled(addr)
}

func clientWebDisabled(addr string) bool {
	switch strings.ToLower(strings.TrimSpace(addr)) {
	case "", "off", "disabled", "none", "false":
		return true
	default:
		return false
	}
}

func writeClientJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
