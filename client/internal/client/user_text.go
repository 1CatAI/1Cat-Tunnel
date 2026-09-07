package client

import "runtime"

// Windows customer deliveries use Chinese text while other platform output
// remains unchanged.
func userText(chinese, fallback string) string {
	if runtime.GOOS == "windows" {
		return chinese
	}
	return fallback
}
