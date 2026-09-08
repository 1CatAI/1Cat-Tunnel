package client

import (
	"testing"
	"tunnel/internal/common"
)

func TestLinuxSetupDefaultsToKeepingCustomMappings(t *testing.T) {
	existing := []common.Preset{{Name: "ssh", LocalAddr: "127.0.0.1:22", Protocol: "tcp"}, {Name: "dns", LocalAddr: "127.0.0.1:53", Protocol: "udp"}}
	if choice := defaultPresetChoice(existing); choice != "5" {
		t.Fatalf("setup would drop custom mappings: default %q", choice)
	}
}
