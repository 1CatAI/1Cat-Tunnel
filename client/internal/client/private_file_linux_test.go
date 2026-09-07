package client

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestMain(m *testing.M) {
	// TempDir requests 0777; a host's group-writable umask must not turn valid
	// config fixtures into deliberately unsafe directories.
	syscall.Umask(0o022)
	os.Exit(m.Run())
}

func TestPrivateFileRejectsHardlinkWithoutChmod(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("private"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Link(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivateFile(link); err == nil {
		t.Fatal("accepted a hardlink")
	}
	info, _ := os.Stat(target)
	if info.Mode().Perm() != 0o644 {
		t.Fatal("changed target permissions")
	}
}
