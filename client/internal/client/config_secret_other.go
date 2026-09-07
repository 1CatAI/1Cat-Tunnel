//go:build !windows

package client

func protectConfigSecretsForStorage(cfg Config) (Config, error) {
	return cfg, nil
}

func restoreConfigSecretsFromStorage(cfg Config) (Config, error) {
	return cfg, nil
}
