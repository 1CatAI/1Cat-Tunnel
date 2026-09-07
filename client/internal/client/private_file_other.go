//go:build !linux && !windows

package client

import "os"

func validatePrivateDirectory(info os.FileInfo) error { return nil }
func openPrivateFile(path string) (*os.File, error)   { return os.Open(path) }
func restrictPrivateFile(f *os.File) error            { return f.Chmod(0o600) }
