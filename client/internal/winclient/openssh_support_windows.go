//go:build windows

package winclient

import (
	"strings"
)

func removeLegacySupportAuthorizedKeyPaths(lines []string) []string {
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) < 2 || !strings.EqualFold(fields[0], "AuthorizedKeysFile") {
			continue
		}
		filtered := make([]string, 0, len(fields)-1)
		for _, path := range fields[1:] {
			if !strings.EqualFold(strings.TrimSpace(path), supportKeyConfigPath) {
				filtered = append(filtered, path)
			}
		}
		if len(filtered) == len(fields)-1 {
			continue
		}
		if len(filtered) == 0 {
			lines[index] = ""
			continue
		}
		indentLength := len(line) - len(strings.TrimLeft(line, " \t"))
		lines[index] = line[:indentLength] + "AuthorizedKeysFile " + strings.Join(filtered, " ")
	}
	return lines
}
