//go:build !windows

package client

import "os"

func replaceFileAtomic(source, target string) error {
	return os.Rename(source, target)
}
