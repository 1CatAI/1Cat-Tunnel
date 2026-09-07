package client

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Permission changes use verified open handles, never a path after closing it.
func validatePrivatePath(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	for p := abs; ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("private path must not contain symbolic links: %s", p)
		}
		if err == nil && info.IsDir() {
			if err := validatePrivateDirectory(info); err != nil {
				return fmt.Errorf("%s: %w", p, err)
			}
		}
		if p == filepath.Dir(p) {
			break
		}
	}
	return nil
}

func readPrivateFile(path string) ([]byte, error) {
	if err := validatePrivatePath(path); err != nil {
		return nil, err
	}
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	f, err := openPrivateFile(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	after, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !after.Mode().IsRegular() || !os.SameFile(before, after) {
		return nil, fmt.Errorf("private file changed or is not a regular file: %s", path)
	}
	if err := restrictPrivateFile(f); err != nil {
		return nil, err
	}
	const maxPrivateFile = 1 << 20
	data, err := io.ReadAll(io.LimitReader(f, maxPrivateFile+1))
	if len(data) > maxPrivateFile {
		return nil, fmt.Errorf("private file exceeds 1 MiB")
	}
	return data, err
}
