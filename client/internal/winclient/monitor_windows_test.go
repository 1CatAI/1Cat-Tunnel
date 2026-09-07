//go:build windows

package winclient

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseStreamConnectionLogLine(t *testing.T) {
	line := `2026/07/30 12:00:00.000000 TCP OPEN {"connection_id":"conn-1","preset":"ssh","remote_addr":"203.0.113.7:50123","local_addr":"127.0.0.1:22"}`
	state, event, ok := parseStreamConnectionLogLine(line)
	if !ok {
		t.Fatal("expected a valid TCP event")
	}
	if state != "OPEN" {
		t.Fatalf("state=%q, want OPEN", state)
	}
	if event.ConnectionID != "conn-1" || event.PresetName != "ssh" {
		t.Fatalf("unexpected event: %+v", event)
	}

	if _, _, ok := parseStreamConnectionLogLine("TCP OPEN not-json"); ok {
		t.Fatal("invalid event must be rejected")
	}
}

func TestConnectionMonitorTracksLiveConnections(t *testing.T) {
	var output bytes.Buffer
	monitor := &connectionMonitor{
		output:       &output,
		loginAccount: "user",
		active:       make(map[string]streamConnectionLogEvent),
	}
	openLine := `TCP OPEN {"connection_id":"conn-1","preset":"ssh","remote_addr":"203.0.113.7:50123","local_addr":"127.0.0.1:22"}`
	closeLine := `TCP CLOSE {"connection_id":"conn-1","preset":"ssh","remote_addr":"203.0.113.7:50123","local_addr":"127.0.0.1:22","duration_ms":1250,"bytes_to_public":2048,"bytes_to_local":4096}`

	monitor.consumeLine(openLine, true)
	if len(monitor.active) != 1 {
		t.Fatalf("active=%d, want 1", len(monitor.active))
	}
	monitor.consumeLine(openLine, true)
	if len(monitor.active) != 1 {
		t.Fatalf("duplicate open changed active count to %d", len(monitor.active))
	}
	monitor.consumeLine(closeLine, true)
	if len(monitor.active) != 0 {
		t.Fatalf("active=%d, want 0", len(monitor.active))
	}

	rendered := output.String()
	for _, expected := range []string{"TCP 已连接", "TCP 已断开", "发送=2.0 KiB", "接收=4.0 KiB"} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("monitor output does not contain %q: %s", expected, rendered)
		}
	}
	if strings.Count(rendered, "TCP 已连接") != 1 {
		t.Fatalf("duplicate OPEN event was rendered more than once: %s", rendered)
	}
}

func TestConnectionMonitorResetsOnServiceRestart(t *testing.T) {
	monitor := &connectionMonitor{
		output:       &bytes.Buffer{},
		loginAccount: "user",
		active: map[string]streamConnectionLogEvent{
			"conn-1": {ConnectionID: "conn-1"},
		},
	}
	monitor.consumeLine("2026/07/30 starting 1CatTunnel Windows service", false)
	if len(monitor.active) != 0 {
		t.Fatalf("service restart left %d stale connection(s)", len(monitor.active))
	}
}

func TestLogFollowerReadsAppendAndRotation(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "client.log")
	if err := os.WriteFile(path, []byte("old line\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	follower, err := newLogFollower(path)
	if err != nil {
		t.Fatal(err)
	}
	appendFile, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := appendFile.WriteString("TCP OPEN {}\n"); err != nil {
		appendFile.Close()
		t.Fatal(err)
	}
	if err := appendFile.Close(); err != nil {
		t.Fatal(err)
	}
	lines, err := follower.readAvailable()
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || lines[0] != "TCP OPEN {}" {
		t.Fatalf("appended lines=%q", lines)
	}

	if err := os.Rename(path, path+".1"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("new generation\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	lines, err = follower.readAvailable()
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || lines[0] != "new generation" {
		t.Fatalf("rotated lines=%q", lines)
	}
}

func TestSSHAssignmentFromLogLine(t *testing.T) {
	line := `preset=SSH protocol=TCP local=127.0.0.1:22 public=dx.1catai.com:52743 hint="ssh -p 52743 user@dx.1catai.com"`
	if got := sshAssignmentFromLogLine(line); got != "dx.1catai.com:52743" {
		t.Fatalf("assignment=%q", got)
	}
}

func TestNormalizeSSHLoginAccount(t *testing.T) {
	tests := map[string]string{
		`OVEN\24241`:          "24241",
		"local-user":          "local-user",
		"SYSTEM":              "",
		tunnelServiceName:     "",
		"  OVEN/local-user  ": "local-user",
	}
	for input, expected := range tests {
		if got := normalizeSSHLoginAccount(input); got != expected {
			t.Errorf("normalizeSSHLoginAccount(%q)=%q, want %q", input, got, expected)
		}
	}
}

func TestSSHLoginAccountIsNeverEmpty(t *testing.T) {
	if account := sshLoginAccount(); strings.TrimSpace(account) == "" {
		t.Fatal("SSH login account must never be empty")
	}
}

func TestParseInstallOptionsEnablesMonitor(t *testing.T) {
	options, err := parseInstallOptions([]string{"--monitor"})
	if err != nil {
		t.Fatal(err)
	}
	if !options.Monitor {
		t.Fatal("--monitor did not enable the resident monitor")
	}
}

func TestSSHConnectionCommandForAccount(t *testing.T) {
	got := sshConnectionCommandForAccount("dx.1catai.com:51007", "24241")
	if got != "ssh -p 51007 24241@dx.1catai.com" {
		t.Fatalf("command=%q", got)
	}
	if got := sshConnectionCommandForAccount("invalid", "24241"); got != "" {
		t.Fatalf("invalid assignment produced %q", got)
	}
}
