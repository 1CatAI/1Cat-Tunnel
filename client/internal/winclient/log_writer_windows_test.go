//go:build windows

package winclient

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRotatingFileWriterBoundsLogFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client.log")
	writer, err := newRotatingFileWriter(path, 80, 2)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 20; index++ {
		if _, err := fmt.Fprintf(writer, "line-%02d-abcdefghijklmnopqrstuvwxyz\n", index); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []string{path, path + ".1", path + ".2"} {
		if _, err := os.Stat(candidate); err != nil {
			t.Fatalf("expected rotated log %s: %v", candidate, err)
		}
	}
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Fatalf("unexpected log beyond retention limit: %v", err)
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(current), "line-19-") {
		t.Fatalf("current log does not contain the newest entry: %s", current)
	}
}

func TestLatestSSHAssignmentReadsRotatedLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client.log")
	if err := os.WriteFile(path, []byte("new log without assignment\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	line := "preset=SSH protocol=TCP local=127.0.0.1:22 public=dx.1catai.com:51234\n"
	if err := os.WriteFile(path+".1", []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := latestSSHAssignment(path); got != "dx.1catai.com:51234" {
		t.Fatalf("assignment = %q", got)
	}
}

func TestLatestSSHAssignmentRejectsPreviousServiceRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client.log")
	contents := strings.Join([]string{
		"preset=SSH protocol=TCP local=127.0.0.1:22 public=dx.1catai.com:50010",
		"starting 1CatTunnel Windows service",
		"client disconnected before receiving assignments",
	}, "\n")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := latestSSHAssignment(path); got != "" {
		t.Fatalf("stale assignment = %q", got)
	}
}

func TestLatestSSHAssignmentUsesCurrentRunAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client.log")
	contents := strings.Join([]string{
		"preset=SSH protocol=TCP local=127.0.0.1:22 public=dx.1catai.com:50010",
		"starting 1CatTunnel Windows service",
		"preset=SSH protocol=TCP local=127.0.0.1:22 public=dx.1catai.com:50011",
	}, "\n")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := latestSSHAssignment(path); got != "dx.1catai.com:50011" {
		t.Fatalf("current assignment = %q", got)
	}
}

func TestQuoteWindowsServiceBinary(t *testing.T) {
	got := quoteWindowsServiceBinary(`C:\Program Files\1CatTunnel\1cattunnel.exe`)
	want := `"C:\Program Files\1CatTunnel\1cattunnel.exe"`
	if got != want {
		t.Fatalf("quoted service binary = %q, want %q", got, want)
	}
}

func TestWaitForClientShutdown(t *testing.T) {
	want := errors.New("stopped")
	done := make(chan error, 1)
	done <- want
	if got := waitForClientShutdown(done, time.Second); !errors.Is(got, want) {
		t.Fatalf("shutdown error = %v, want %v", got, want)
	}

	if err := waitForClientShutdown(make(chan error), 10*time.Millisecond); err == nil {
		t.Fatal("expected a shutdown timeout")
	}
}
