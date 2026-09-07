package client

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"tunnel/internal/common"
)

func TestPersistIssuedCredentialReplacesEnrollmentPassword(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client.json")
	c := &Client{cfg: Config{
		ServerAddr:         "dx.1catai.com:50001",
		Token:              "shared-enrollment-password",
		EnrollmentPassword: "shared-enrollment-password",
		NodeName:           "node-1",
		TLSEnabled:         true,
		configPath:         path,
	}}

	if err := c.persistIssuedCredential("dedicated-node-token"); err != nil {
		t.Fatalf("persistIssuedCredential returned error: %v", err)
	}

	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved config: %v", err)
	}
	var saved Config
	if err := json.Unmarshal(payload, &saved); err != nil {
		t.Fatalf("decode saved config: %v", err)
	}
	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load saved config: %v", err)
	}
	if loaded.Token != "dedicated-node-token" {
		t.Fatalf("loaded token = %q", loaded.Token)
	}
	if saved.EnrollmentPassword != "" || saved.BootstrapToken != "" {
		t.Fatal("shared/bootstrap credential was not cleared")
	}

	if runtime.GOOS == "windows" {
		if saved.Token != "" || saved.ProtectedToken == "" {
			t.Fatal("Windows config did not replace the plaintext node token with DPAPI data")
		}
		if bytes.Contains(payload, []byte("dedicated-node-token")) {
			t.Fatal("Windows config leaked the node token in plaintext")
		}
	} else {
		if saved.Token != "dedicated-node-token" {
			t.Fatalf("saved token = %q", saved.Token)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat saved config: %v", err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("config mode = %o, want 600", info.Mode().Perm())
		}
	}
}

func TestSaveConfigAtomicallyReplacesExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client.json")
	base := Config{
		ServerAddr:           "dx.1catai.com:50001",
		Token:                "first-token",
		NodeName:             "atomic-config-test",
		ReconnectIntervalSec: 5,
		TLSEnabled:           true,
		Presets: []common.Preset{{
			Name:      "ssh",
			LocalAddr: "127.0.0.1:22",
			Protocol:  common.NetworkTCP,
		}},
	}
	if err := SaveConfig(path, base); err != nil {
		t.Fatal(err)
	}
	base.Token = "second-token"
	if err := SaveConfig(path, base); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Token != "second-token" {
		t.Fatalf("loaded token = %q", loaded.Token)
	}
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".1cat-config-*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary config files remain: %v", matches)
	}
}

func TestWindowsSSHWizardUsesLoopbackDialTarget(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-specific preset")
	}
	options := presetOptionsForWizard(nil)
	if len(options) < 2 || len(options[1].Presets) != 1 {
		t.Fatal("SSH wizard preset is missing")
	}
	if got := options[1].Presets[0].LocalAddr; got != "127.0.0.1:22" {
		t.Fatalf("Windows SSH target = %q, want 127.0.0.1:22", got)
	}
}

func TestBuildClientTLSConfigDerivesServerName(t *testing.T) {
	config, err := buildClientTLSConfig(Config{
		ServerAddr: "dx.1catai.com:50001",
		TLSEnabled: true,
	})
	if err != nil {
		t.Fatalf("buildClientTLSConfig returned error: %v", err)
	}
	if config == nil || config.ServerName != "dx.1catai.com" {
		t.Fatalf("TLS server name = %q", config.ServerName)
	}
}

func TestDefaultPublicServerUsesPrimaryProductionControlPort(t *testing.T) {
	if defaultPublicServerAddr != "dx.1catai.com:50001" {
		t.Fatalf("default public server = %q", defaultPublicServerAddr)
	}
}

func TestReadMaskedValueDoesNotEchoCredential(t *testing.T) {
	input := bytes.NewBufferString("secrex\bt\r")
	var output bytes.Buffer

	value, err := readMaskedValue(input, &output)
	if err != nil {
		t.Fatalf("readMaskedValue returned error: %v", err)
	}
	if value != "secret" {
		t.Fatalf("value = %q, want secret", value)
	}
	if strings.Contains(output.String(), value) {
		t.Fatalf("masked output leaked credential: %q", output.String())
	}
	if output.String() != "******\b \b*" {
		t.Fatalf("masked output = %q", output.String())
	}
}

func TestSecretPromptDoesNotRevealSavedCredential(t *testing.T) {
	prompt := formatPrompt("Access token", "SuperSecret", "credential", true)
	if strings.Contains(prompt, "SuperSecret") {
		t.Fatalf("secret prompt leaked saved credential: %q", prompt)
	}
	if !strings.Contains(prompt, userText("已保存；直接按回车保留", "saved; press Enter to keep")) {
		t.Fatalf("secret prompt does not explain saved credential behavior: %q", prompt)
	}
}

func TestMaskTokenHidesEntireCredential(t *testing.T) {
	if got := maskToken("SuperSecret"); got != "********" {
		t.Fatalf("maskToken = %q", got)
	}
}
