package launcher

import (
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSetupLoggingFallsBackFromUnwritableProgramLocation(t *testing.T) {
	primaryDir := t.TempDir()
	fallbackDir := filepath.Join(t.TempDir(), "nested", "logs")
	fileName := "tunnel-client.log"

	// A directory at the primary log path deterministically simulates a
	// read-only or otherwise unusable global npm installation directory.
	if err := os.Mkdir(filepath.Join(primaryDir, fileName), 0o700); err != nil {
		t.Fatal(err)
	}

	oldWriter := log.Writer()
	defer log.SetOutput(oldWriter)
	cleanup, path, err := setupLoggingWithFallback(fileName, primaryDir, fallbackDir)
	if err != nil {
		t.Fatalf("setupLoggingWithFallback: %v", err)
	}
	wantPath := filepath.Join(fallbackDir, fileName)
	if !samePath(path, wantPath) {
		t.Fatalf("log path = %q, want %q", path, wantPath)
	}
	log.Print("fallback-log-probe")
	cleanup()

	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), "fallback-log-probe") {
		t.Fatalf("fallback log does not contain probe: %q", payload)
	}
	if runtime.GOOS != "windows" {
		dirInfo, err := os.Stat(fallbackDir)
		if err != nil {
			t.Fatal(err)
		}
		if dirInfo.Mode().Perm() != 0o700 {
			t.Fatalf("fallback directory mode = %v", dirInfo.Mode().Perm())
		}
		logInfo, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if logInfo.Mode().Perm() != 0o600 {
			t.Fatalf("fallback log mode = %v", logInfo.Mode().Perm())
		}
	}
}

func TestSetupLoggingRejectsPathLikeFileName(t *testing.T) {
	if _, _, err := setupLoggingWithFallback(filepath.Join("subdir", "secret.log"), t.TempDir(), t.TempDir()); err == nil {
		t.Fatal("expected path-like log file name to be rejected")
	}
}
