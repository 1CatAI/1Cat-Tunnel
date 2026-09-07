package client

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagementRequiresOwnerBeforeRevealingState(t *testing.T) {
	c := &Client{cfg: Config{NodeName: "private-node", configPath: filepath.Join(t.TempDir(), "client.json")}, webCSRFToken: "private-csrf"}
	if err := c.initializeManagementAccess(); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", c.handleManagementLogin)
	mux.HandleFunc("/api/view", c.handleManagementDashboard)
	mux.HandleFunc("/api/status", c.handleManagementStatus)
	mux.HandleFunc("/api/config", c.handleManagementConfig)
	mux.HandleFunc("/api/servers", c.handleManagementServers)
	mux.HandleFunc("/api/reconnect", c.handleManagementReconnect)
	handler := localManagementOnly(c.authenticateManagement(mux))
	for _, endpoint := range []string{"/api/view", "/api/status", "/api/config", "/api/servers", "/api/reconnect"} {
		for _, key := range []string{"", strings.Repeat("b", 64)} {
			req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:51888"+endpoint, nil)
			req.RemoteAddr = "127.0.0.1:12345"
			req.Header.Set("Authorization", "Bearer "+key)
			r := httptest.NewRecorder()
			handler.ServeHTTP(r, req)
			if r.Code != 401 || strings.Contains(r.Body.String(), "private-node") || strings.Contains(r.Body.String(), "private-csrf") {
				t.Fatalf("%s leaked state: %d", endpoint, r.Code)
			}
		}
	}
	for _, endpoint := range []string{"/", "/api/view", "/api/status"} {
		req := httptest.NewRequest("GET", "http://127.0.0.1:51888"+endpoint, nil)
		req.RemoteAddr = "127.0.0.1:12345"
		if endpoint != "/" {
			req.Header.Set("Authorization", "Bearer "+c.webAccessToken)
		}
		r := httptest.NewRecorder()
		handler.ServeHTTP(r, req)
		if r.Code != 200 {
			t.Fatalf("%s returned %d", endpoint, r.Code)
		}
		if endpoint == "/" && (strings.Contains(r.Body.String(), "private-node") || strings.Contains(r.Body.String(), "private-csrf")) {
			t.Fatal("public login leaked metadata")
		}
	}
	old := c.webAccessToken
	if err := c.initializeManagementAccess(); err != nil {
		t.Fatal(err)
	}
	if old == c.webAccessToken {
		t.Fatal("key did not rotate")
	}
	key, err := managementKey(c.cfg)
	if err != nil || key != c.webAccessToken {
		t.Fatalf("owner cannot recover key: %v", err)
	}
}

func TestSecondClientDoesNotReplaceManagementKey(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	path := filepath.Join(t.TempDir(), "client.json")
	c := &Client{cfg: Config{configPath: path, WebListenAddr: listener.Addr().String()}}
	if err := c.initializeManagementAccess(); err != nil {
		t.Fatal(err)
	}
	old, _ := os.ReadFile(path + ".web-auth.json")
	if err := c.startLocalWeb(context.Background()); err == nil {
		t.Fatal("duplicate bind succeeded")
	}
	current, _ := os.ReadFile(path + ".web-auth.json")
	if string(old) != string(current) {
		t.Fatal("duplicate startup replaced running key")
	}
}

func TestPrivateConfigRejectsLinksAndPreservesTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	if err := os.WriteFile(target, []byte(`{"node_name":"untouched"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "client.json")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := InitializeManagedConfig(link); err == nil {
		t.Fatal("accepted symlink")
	}
	if err := SaveConfig(link, Config{NodeName: "changed"}); err == nil {
		t.Fatal("wrote through symlink")
	}
	var cfg Config
	data, _ := os.ReadFile(target)
	_ = json.Unmarshal(data, &cfg)
	if cfg.NodeName != "untouched" {
		t.Fatal("modified link target")
	}
}
