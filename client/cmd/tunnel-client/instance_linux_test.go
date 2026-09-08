package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestRuntimeFilesRejectLinks(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("unchanged"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, link := range []func(string, string) error{os.Symlink, os.Link} {
		path := filepath.Join(dir, "linked")
		if err := link(target, path); err != nil {
			t.Fatal(err)
		}
		if f, err := openRuntimeFile(path); err == nil {
			f.Close()
			t.Fatal("linked runtime file accepted")
		}
		os.Remove(path)
	}
	data, _ := os.ReadFile(target)
	if string(data) != "unchanged" {
		t.Fatal("target changed")
	}
}

func TestServiceCannotReplaceForegroundLock(t *testing.T) {
	config := filepath.Join(t.TempDir(), "client.json")
	unlock, err := acquireInstance(context.Background(), config, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquireInstance(context.Background(), config, false); !errors.Is(err, errAlreadyRunning) {
		t.Fatalf("second service: %v", err)
	}
	unlock()
	unlock, err = acquireInstance(context.Background(), config, false)
	if err != nil {
		t.Fatal(err)
	}
	unlock()
}

func TestMonitorLockIndependentOfClient(t *testing.T) {
	config := filepath.Join(t.TempDir(), "client.json")
	unlock, err := acquireInstance(context.Background(), config, false)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	observer, err := lockFile(context.Background(), config+".monitor.lock", false)
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close()
	if _, err := lockFile(context.Background(), config+".monitor.lock", false); !errors.Is(err, unix.EWOULDBLOCK) {
		t.Fatalf("duplicate observer: %v", err)
	}
}

func TestRuntimeLogBound(t *testing.T) {
	f, err := openRuntimeFile(filepath.Join(t.TempDir(), "client.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := &boundedRuntimeLog{file: f}
	data := make([]byte, 1<<20)
	for range 20 {
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	info, _ := f.Stat()
	if info.Size() > 8<<20 {
		t.Fatal("unbounded log")
	}
}
