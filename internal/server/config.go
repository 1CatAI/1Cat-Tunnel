package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	defaultControlListenAddr   = ":7000"
	defaultHTTPListenAddr      = ":8080"
	defaultPublicBindAddr      = "0.0.0.0"
	defaultHandshakeTimeoutSec = 10
	defaultAttachTimeoutSec    = 15
	defaultAutoPortStart       = 50000
	defaultAutoPortEnd         = 55000
	defaultUDPSessionTimeout   = 60
	defaultStateSaveInterval   = 5
)

type Config struct {
	ControlListenAddr    string `json:"control_listen_addr"`
	HTTPListenAddr       string `json:"http_listen_addr"`
	PublicBindAddr       string `json:"public_bind_addr"`
	PublicHost           string `json:"public_host"`
	AdminUsername        string `json:"admin_username"`
	AdminPassword        string `json:"admin_password"`
	HandshakeTimeoutSec  int    `json:"handshake_timeout_sec"`
	AttachTimeoutSec     int    `json:"attach_timeout_sec"`
	AutoPortStart        int    `json:"auto_port_start"`
	AutoPortEnd          int    `json:"auto_port_end"`
	UDPSessionTimeoutSec int    `json:"udp_session_timeout_sec"`
	StateSaveIntervalSec int    `json:"state_save_interval_sec"`
	StateFile            string `json:"state_file"`
}

func LoadConfig(path string) (Config, error) {
	cfg := Config{
		ControlListenAddr:    defaultControlListenAddr,
		HTTPListenAddr:       defaultHTTPListenAddr,
		PublicBindAddr:       defaultPublicBindAddr,
		HandshakeTimeoutSec:  defaultHandshakeTimeoutSec,
		AttachTimeoutSec:     defaultAttachTimeoutSec,
		AutoPortStart:        defaultAutoPortStart,
		AutoPortEnd:          defaultAutoPortEnd,
		UDPSessionTimeoutSec: defaultUDPSessionTimeout,
		StateSaveIntervalSec: defaultStateSaveInterval,
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}

	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}

	if cfg.ControlListenAddr == "" {
		cfg.ControlListenAddr = defaultControlListenAddr
	}
	if cfg.HTTPListenAddr == "" {
		cfg.HTTPListenAddr = defaultHTTPListenAddr
	}
	if cfg.PublicBindAddr == "" {
		cfg.PublicBindAddr = defaultPublicBindAddr
	}
	if containsPlaceholder(cfg.PublicHost, "YOUR_PUBLIC_IP_OR_DOMAIN") {
		return Config{}, fmt.Errorf("public_host still contains the example placeholder, please edit server config first")
	}
	if cfg.HandshakeTimeoutSec <= 0 {
		cfg.HandshakeTimeoutSec = defaultHandshakeTimeoutSec
	}
	if cfg.AttachTimeoutSec <= 0 {
		cfg.AttachTimeoutSec = defaultAttachTimeoutSec
	}
	if cfg.AutoPortStart <= 0 {
		cfg.AutoPortStart = defaultAutoPortStart
	}
	if cfg.AutoPortEnd <= 0 {
		cfg.AutoPortEnd = defaultAutoPortEnd
	}
	if cfg.AutoPortEnd < cfg.AutoPortStart {
		return Config{}, fmt.Errorf("auto_port_end must be greater than or equal to auto_port_start")
	}
	if cfg.UDPSessionTimeoutSec <= 0 {
		cfg.UDPSessionTimeoutSec = defaultUDPSessionTimeout
	}
	if cfg.StateSaveIntervalSec <= 0 {
		cfg.StateSaveIntervalSec = defaultStateSaveInterval
	}
	if strings.TrimSpace(cfg.StateFile) == "" {
		cfg.StateFile = filepath.Join(filepath.Dir(path), "server-state.json")
	} else if !filepath.IsAbs(cfg.StateFile) {
		cfg.StateFile = filepath.Join(filepath.Dir(path), cfg.StateFile)
	}
	if cfg.AdminUsername == "" {
		return Config{}, fmt.Errorf("admin_username is required")
	}
	if cfg.AdminPassword == "" {
		return Config{}, fmt.Errorf("admin_password is required")
	}
	if containsPlaceholder(cfg.AdminPassword, "change-this-password") {
		return Config{}, fmt.Errorf("admin_password still contains the example placeholder, please edit server config first")
	}

	return cfg, nil
}

func containsPlaceholder(value, marker string) bool {
	return strings.Contains(strings.ToUpper(value), strings.ToUpper(marker))
}
