package client

import (
	"fmt"
	"golang.org/x/sys/unix"
	"os"
	"syscall"
)

func validatePrivateDirectory(info os.FileInfo) error {
	stat := info.Sys().(*syscall.Stat_t)
	if stat.Uid != 0 && stat.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("private directory has a different owner")
	}
	if info.Mode().Perm()&0o022 != 0 && !(stat.Uid == 0 && info.Mode()&os.ModeSticky != 0) {
		return fmt.Errorf("private directory is writable by other users")
	}
	return nil
}

func openPrivateFile(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(fd), path), nil
}

func restrictPrivateFile(f *os.File) error {
	var stat unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &stat); err != nil {
		return err
	}
	if stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
		return fmt.Errorf("private file must be owned by the current user and have exactly one link")
	}
	return f.Chmod(0o600)
}
