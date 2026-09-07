package client

func protectManagementKey(key string) (string, error) {
	return protectWindowsSecretWithFlags(key, false)
}
func restoreManagementKey(key string) (string, error) { return unprotectWindowsSecret(key) }
