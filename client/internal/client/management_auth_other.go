//go:build !windows

package client

func protectManagementKey(key string) (string, error) { return key, nil }
func restoreManagementKey(key string) (string, error) { return key, nil }
