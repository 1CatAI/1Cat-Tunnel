package client

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"tunnel/internal/common"
)

func TestLoadManagedConfigStartsWithSafeDefaultsWhenMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "client-linux.json")
	cfg, err := LoadManagedConfig(path)
	if err != nil {
		t.Fatalf("LoadManagedConfig returned error: %v", err)
	}
	if cfg.configPath != path {
		t.Fatalf("config path = %q, want %q", cfg.configPath, path)
	}
	if cfg.WebListenAddr != defaultClientWebListenAddr {
		t.Fatalf("web listen address = %q", cfg.WebListenAddr)
	}
	if cfg.SelectedServerID != primaryHostedServerID {
		t.Fatalf("selected server = %q", cfg.SelectedServerID)
	}
	if len(cfg.Presets) == 0 {
		t.Fatal("managed config did not create a default local mapping")
	}
}

func TestValidateClientWebListenAddrOnlyAllowsLoopback(t *testing.T) {
	for _, address := range []string{"127.0.0.1:51888", "localhost:51888", "[::1]:51888"} {
		if err := validateClientWebListenAddr(address); err != nil {
			t.Fatalf("loopback address %q rejected: %v", address, err)
		}
	}
	for _, address := range []string{"0.0.0.0:51888", "192.168.1.10:51888", "example.com:51888"} {
		if err := validateClientWebListenAddr(address); err == nil {
			t.Fatalf("non-loopback address %q was accepted", address)
		}
	}
}

func TestManagedPresetsUseCountAndLocalTargetsOnly(t *testing.T) {
	presets, err := managedPresetsFromRequest(managedConfigRequest{
		MappingCount: 2,
		Mappings: []managedMappingRequest{
			{Name: "ssh", Protocol: "tcp", LocalHost: "127.0.0.1", LocalPort: 22},
			{Name: "dns", Protocol: "udp", LocalHost: "::1", LocalPort: 53},
		},
	})
	if err != nil {
		t.Fatalf("managedPresetsFromRequest returned error: %v", err)
	}
	if len(presets) != 2 {
		t.Fatalf("preset count = %d, want 2", len(presets))
	}
	if presets[0].LocalAddr != "127.0.0.1:22" || presets[1].LocalAddr != "[::1]:53" {
		t.Fatalf("unexpected local targets: %+v", presets)
	}
	if presets[0].Protocol != common.NetworkTCP || presets[1].Protocol != common.NetworkUDP {
		t.Fatalf("unexpected protocols: %+v", presets)
	}

	_, err = managedPresetsFromRequest(managedConfigRequest{
		MappingCount: 2,
		Mappings: []managedMappingRequest{
			{Name: "ssh", Protocol: "tcp", LocalHost: "127.0.0.1", LocalPort: 22},
		},
	})
	if err == nil {
		t.Fatal("mapping count mismatch was accepted")
	}

	_, err = managedPresetsFromRequest(managedConfigRequest{
		MappingCount: 1,
		Mappings: []managedMappingRequest{
			{Name: "bad", Protocol: "tcp", LocalHost: "127.0.0.1:22", LocalPort: 8080},
		},
	})
	if err == nil {
		t.Fatal("host containing a second port was accepted")
	}
}

func TestManagementConfigSavesMappingsAndRequestsReconnect(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client.json")
	cfg, err := applyHostedServer(prepareBootstrapConfig(Config{
		Token:                "dedicated-node-token",
		NodeName:             "managed-node",
		ReconnectIntervalSec: 5,
		Presets: []common.Preset{{
			Name:      "ssh",
			LocalAddr: "127.0.0.1:22",
			Protocol:  common.NetworkTCP,
		}},
	}), primaryHostedServerID)
	if err != nil {
		t.Fatal(err)
	}
	cfg.configPath = path
	if err := SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	cfg, err = LoadManagedConfig(path)
	if err != nil {
		t.Fatal(err)
	}

	client := &Client{cfg: cfg, webCSRFToken: "test-csrf"}
	client.initialize()
	payload := managedConfigRequest{
		ServerID:     primaryHostedServerID,
		NodeName:     "managed-node",
		MappingCount: 2,
		Mappings: []managedMappingRequest{
			{Name: "ssh", Protocol: "tcp", LocalHost: "127.0.0.1", LocalPort: 22},
			{Name: "web", Protocol: "tcp", LocalHost: "127.0.0.1", LocalPort: 8080},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:51888/api/config", bytes.NewReader(body))
	req.RemoteAddr = "127.0.0.1:54321"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-1Cat-CSRF", "test-csrf")
	req.Header.Set("Origin", "http://127.0.0.1:51888")
	response := httptest.NewRecorder()

	client.handleManagementConfig(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("reload saved config: %v", err)
	}
	if len(loaded.Presets) != 2 || loaded.Presets[1].Name != "web" || loaded.Presets[1].LocalAddr != "127.0.0.1:8080" {
		t.Fatalf("saved mappings = %+v", loaded.Presets)
	}
	select {
	case <-client.restart:
	default:
		t.Fatal("configuration update did not request a reconnect")
	}

	payload.NodeName = "renamed-node"
	body, err = json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	renameRequest := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:51888/api/config", bytes.NewReader(body))
	renameRequest.RemoteAddr = "127.0.0.1:54322"
	renameRequest.Header.Set("Content-Type", "application/json")
	renameRequest.Header.Set("X-1Cat-CSRF", "test-csrf")
	renameRequest.Header.Set("Origin", "http://127.0.0.1:51888")
	renameResponse := httptest.NewRecorder()
	client.handleManagementConfig(renameResponse, renameRequest)
	if renameResponse.Code != http.StatusBadRequest {
		t.Fatalf("rename without a new credential status = %d, body = %s", renameResponse.Code, renameResponse.Body.String())
	}
}

func TestManagementMutationRejectsRemoteRequests(t *testing.T) {
	client := &Client{webCSRFToken: "test-csrf"}
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:51888/api/reconnect", strings.NewReader("{}"))
	req.RemoteAddr = "192.0.2.10:4567"
	req.Header.Set("X-1Cat-CSRF", "test-csrf")
	if err := client.validateManagementMutation(req); err == nil {
		t.Fatal("remote management mutation was accepted")
	}
}

func TestLocalManagementListenerPreventsDuplicateClient(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := probe.Addr().String()
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	first := &Client{cfg: prepareBootstrapConfig(Config{WebListenAddr: address})}
	first.initialize()
	if err := first.startLocalWeb(ctx); err != nil {
		cancel()
		t.Fatalf("first management listener failed: %v", err)
	}

	second := &Client{cfg: prepareBootstrapConfig(Config{WebListenAddr: address})}
	second.initialize()
	if err := second.startLocalWeb(context.Background()); err == nil {
		cancel()
		t.Fatal("second management listener unexpectedly succeeded")
	}
	cancel()
	time.Sleep(20 * time.Millisecond)
}

func TestManagedClientStopsPromptlyWhenContextIsCanceled(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := probe.Addr().String()
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	client := &Client{cfg: prepareBootstrapConfig(Config{WebListenAddr: address})}
	done := make(chan error, 1)
	go func() {
		done <- client.Run(ctx)
	}()

	deadline := time.Now().Add(time.Second)
	for {
		connection, dialErr := net.DialTimeout("tcp", address, 50*time.Millisecond)
		if dialErr == nil {
			_ = connection.Close()
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("management listener did not start: %v", dialErr)
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("managed client returned error after cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("managed client did not stop after context cancellation")
	}
}

func TestMappingEditPreservesExistingTLSSettings(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		cfg := Config{ServerAddr: defaultPublicServerAddr, TLSEnabled: enabled, Token: "dedicated", NodeName: "existing"}
		updated, err := applyHostedServer(cfg, primaryHostedServerID)
		if err != nil || !sameHostedServerConnection(cfg, updated) {
			t.Fatalf("ordinary mapping edit changed trust settings: %+v, %v", updated, err)
		}
	}
}

func TestLateEnrollmentCannotOverwriteAnotherServerCredential(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client.json")
	source := prepareBootstrapConfig(Config{ServerAddr: "first.example:50001", NodeName: "node", Token: "enrollment", configPath: path})
	c := &Client{cfg: source}
	c.initialize()
	next := source
	next.ServerAddr, next.Token = "second.example:50001", "second-dedicated"
	if err := c.replaceConfig(next); err != nil {
		t.Fatal(err)
	}
	if err := c.persistSessionCredential(source, "first-issued"); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Token != "second-dedicated" || loaded.ServerAddr != next.ServerAddr {
		t.Fatal("late enrollment overwrote the new server identity")
	}
}

func TestCredentialPersistenceKeepsConcurrentMappingEdits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client.json")
	source := prepareBootstrapConfig(Config{ServerAddr: "first.example:50001", NodeName: "node", Token: "enrollment", configPath: path})
	c := &Client{cfg: source}
	c.initialize()
	var wg sync.WaitGroup
	for index := 0; index < 12; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.updateMu.Lock()
			next := c.currentConfig()
			next.Presets = []common.Preset{{Name: "web", Protocol: "tcp", LocalAddr: "127.0.0.1:8080"}}
			err := c.replaceConfigLocked(next)
			c.updateMu.Unlock()
			if err != nil {
				t.Error(err)
			}
		}()
	}
	if err := c.persistSessionCredential(source, "issued-dedicated"); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Token != "issued-dedicated" || len(loaded.Presets) != 1 || loaded.Presets[0].Name != "web" {
		t.Fatal("concurrent configuration changes were lost")
	}
}

func TestFailedCredentialSaveDoesNotReplaceInMemoryIdentity(t *testing.T) {
	dir := t.TempDir()
	parent := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(parent, []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	c := &Client{cfg: Config{Token: "original", configPath: filepath.Join(parent, "client.json")}}
	if err := c.persistIssuedCredential("new-token"); err == nil {
		t.Fatal("expected write failure")
	}
	if c.currentConfig().Token != "original" {
		t.Fatal("failed save changed in-memory identity")
	}
}

func TestLocalManagementRejectsDNSRebinding(t *testing.T) {
	handler := localManagementOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	for _, host := range []string{"evil.example:51888", "127.0.0.1.evil.example:51888", "192.168.0.1:51888"} {
		req := httptest.NewRequest("GET", "http://"+host+"/", nil)
		req.RemoteAddr = "127.0.0.1:43210"
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code != http.StatusForbidden {
			t.Fatalf("accepted rebinding host %q", host)
		}
	}
	for _, host := range []string{"127.0.0.1:51888", "localhost:51999", "[::1]:51888"} {
		req := httptest.NewRequest("GET", "http://"+host+"/", nil)
		req.RemoteAddr = "127.0.0.1:43210"
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code != http.StatusOK {
			t.Fatalf("rejected loopback host %q", host)
		}
	}
}

func TestCustomHostedServerLifecycle(t *testing.T) {
	cfg := prepareBootstrapConfig(Config{ServerAddr: defaultPublicServerAddr, Token: "original"})
	next, server, err := editHostedServers(cfg, managedServerRequest{Action: "add", Name: "second", ServerAddr: "second.example:50001"})
	if err != nil {
		t.Fatal(err)
	}
	if next.Token != cfg.Token || next.ServerAddr != cfg.ServerAddr || !server.TLSEnabled {
		t.Fatal("adding a server changed the active connection")
	}
	selected, err := applyHostedServer(next, server.ID)
	if err != nil || selected.ServerAddr != "second.example:50001" {
		t.Fatal("custom server not selectable")
	}
	if _, _, err := editHostedServers(selected, managedServerRequest{Action: "remove", ID: server.ID}); err == nil {
		t.Fatal("removed active server")
	}
	removed, _, err := editHostedServers(next, managedServerRequest{Action: "remove", ID: server.ID})
	if err != nil || len(removed.HostedServers) != 0 {
		t.Fatal("custom server not removable")
	}
	if len(next.HostedServers) != 1 {
		t.Fatal("server slice alias changed prior config")
	}
	if _, _, err := editHostedServers(cfg, managedServerRequest{Action: "add", Name: "bad", ServerAddr: "https://second.example:50001"}); err == nil {
		t.Fatal("accepted URL instead of control address")
	}
}

func TestDisconnectedMappingsAreNotAdvertisedAsReady(t *testing.T) {
	c := &Client{cfg: Config{Presets: []common.Preset{{Name: "ssh", LocalAddr: "127.0.0.1:22"}}}}
	c.initialize()
	c.updateAssignments(common.AssignmentsMessage{Tunnels: []common.AssignedTunnel{{PresetName: "ssh", PublicTarget: "example.test:50002"}}})
	if c.managementStatusSnapshot().Mappings[0].PublicTarget != "" {
		t.Fatal("offline mapping was reported as ready")
	}
}
