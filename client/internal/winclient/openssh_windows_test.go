//go:build windows

package winclient

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"tunnel/internal/client"
	"tunnel/internal/common"
)

func TestBuildManagedSSHDConfigInsertsGlobalSettingsBeforeMatch(t *testing.T) {
	input := "# default\r\n#ListenAddress 0.0.0.0\r\n\r\nMatch Group administrators\r\n    AuthorizedKeysFile __PROGRAMDATA__/ssh/administrators_authorized_keys\r\n"
	output, err := buildManagedSSHDConfig(input)
	if err != nil {
		t.Fatalf("buildManagedSSHDConfig returned error: %v", err)
	}
	managedIndex := strings.Index(output, managedSSHConfigBegin)
	matchIndex := strings.Index(output, "Match Group administrators")
	if managedIndex < 0 || matchIndex < 0 || managedIndex > matchIndex {
		t.Fatalf("managed global block was not inserted before Match:\n%s", output)
	}
	if strings.Count(output, "ListenAddress 127.0.0.1") != 1 {
		t.Fatalf("managed config has an unexpected loopback listener count:\n%s", output)
	}
}

func TestBuildManagedSSHDConfigIsIdempotent(t *testing.T) {
	first, err := buildManagedSSHDConfig("# default\n")
	if err != nil {
		t.Fatal(err)
	}
	second, err := buildManagedSSHDConfig(first)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("managed sshd_config is not idempotent\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

func TestBuildManagedSSHDConfigBoundsUnauthenticatedConnections(t *testing.T) {
	output, err := buildManagedSSHDConfig("# default\n")
	if err != nil {
		t.Fatal(err)
	}
	for _, directive := range []string{
		"LoginGraceTime 30",
		"MaxAuthTries 4",
		"MaxStartups 16:50:32",
	} {
		if countExactConfigLine(output, directive) != 1 {
			t.Fatalf("managed config does not contain %q exactly once:\n%s", directive, output)
		}
	}
}

func TestBuildManagedSSHDConfigRejectsExistingListener(t *testing.T) {
	_, err := buildManagedSSHDConfig("ListenAddress 0.0.0.0\n")
	if err == nil {
		t.Fatal("expected an active ListenAddress to require manual review")
	}
}

func TestBuildManagedSSHDConfigRejectsTabbedListener(t *testing.T) {
	_, err := buildManagedSSHDConfig("ListenAddress\t0.0.0.0\n")
	if err == nil {
		t.Fatal("expected a tab-separated ListenAddress to require manual review")
	}
}

func TestBuildManagedSSHDConfigRejectsUnmatchedMarkers(t *testing.T) {
	for _, input := range []string{
		managedSSHConfigEnd + "\n",
		managedSSHConfigBegin + "\n" + managedSSHConfigBegin + "\n",
	} {
		if _, err := buildManagedSSHDConfig(input); err == nil {
			t.Fatalf("expected malformed managed markers to fail: %q", input)
		}
	}
}

func TestBuildManagedSSHDConfigRejectsExistingAuthorizedKeysCommand(t *testing.T) {
	for _, input := range []string{
		"AuthorizedKeysCommand C:/custom/helper.exe\n",
		"AuthorizedKeysCommandUser custom-user\n",
	} {
		if _, err := buildManagedSSHDConfigForExecutable(input, "C:\\Program Files\\1CatTunnel\\1cattunnel.exe"); err == nil {
			t.Fatalf("expected existing command directive to require review: %q", input)
		}
	}
}

func TestBuildManagedSSHDConfigRejectsUnsafeCommandPath(t *testing.T) {
	for _, path := range []string{"relative.exe", "C:\\bad\"path\\helper.exe", "C:\\bad\npath\\helper.exe"} {
		if _, err := buildManagedSSHDConfigForExecutable("# default\n", path); err == nil {
			t.Fatalf("expected unsafe command path %q to be rejected", path)
		}
	}
}

func TestBuildManagedSSHDConfigInsertsBeforeTabbedMatch(t *testing.T) {
	output, err := buildManagedSSHDConfig("# default\nMatch\tGroup administrators\n")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Index(output, managedSSHConfigBegin) > strings.Index(output, "Match\tGroup") {
		t.Fatalf("managed block was inserted inside the Match block:\n%s", output)
	}
}

func TestEnsureSSHPresetPreservesOtherMappings(t *testing.T) {
	cfg := client.Config{Presets: []common.Preset{
		{Name: "rdp", LocalAddr: "127.0.0.1:3389", Protocol: common.NetworkTCP},
		{Name: "ssh", LocalAddr: "0.0.0.0:22", Protocol: common.NetworkTCP},
	}}
	updated := ensureSSHPreset(cfg)
	if len(updated.Presets) != 2 {
		t.Fatalf("preset count = %d, want 2", len(updated.Presets))
	}
	if updated.Presets[1].LocalAddr != "127.0.0.1:22" {
		t.Fatalf("SSH target = %q", updated.Presets[1].LocalAddr)
	}
	if updated.Presets[0].Name != "rdp" {
		t.Fatal("existing RDP mapping was not preserved")
	}
}

func TestVerifyOpenSSHSignaturesIntegration(t *testing.T) {
	target := strings.TrimSpace(os.Getenv("ONECAT_TEST_OPENSSH_ROOT"))
	if target == "" {
		t.Skip("set ONECAT_TEST_OPENSSH_ROOT to verify a real OpenSSH payload")
	}
	powershell, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := verifyOpenSSHSignatures(ctx, powershell, target); err != nil {
		t.Fatal(err)
	}
	if err := verifyPinnedOpenSSHVersion(ctx, target); err != nil {
		t.Fatal(err)
	}
}

func FuzzBuildManagedSSHDConfig(f *testing.F) {
	f.Add("# default\n")
	f.Add("Match Group administrators\n    AuthorizedKeysFile test\n")
	f.Add(managedSSHConfigBegin + "\nListenAddress 127.0.0.1\n" + managedSSHConfigEnd + "\n")
	f.Fuzz(func(t *testing.T, input string) {
		output, err := buildManagedSSHDConfig(input)
		if err != nil {
			return
		}
		if countExactConfigLine(output, managedSSHConfigBegin) != 1 || countExactConfigLine(output, managedSSHConfigEnd) != 1 {
			t.Fatalf("successful config has invalid managed marker count:\n%s", output)
		}
		again, err := buildManagedSSHDConfig(output)
		if err != nil {
			t.Fatalf("generated config is not parseable: %v", err)
		}
		if output != again {
			t.Fatalf("generated config is not idempotent")
		}
	})
}

func countExactConfigLine(config, target string) int {
	count := 0
	for _, line := range strings.Split(strings.ReplaceAll(config, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(line) == target {
			count++
		}
	}
	return count
}
