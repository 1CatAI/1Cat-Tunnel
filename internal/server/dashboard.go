package server

import (
	"fmt"
	"net"
	"strings"

	"tunnel/internal/common"
)

func (s *Server) printStartupDashboard() {
	webURL := s.publicWebURL()
	lines := []string{
		common.Version + " started",
		"",
		"Control listen: " + s.cfg.ControlListenAddr,
		"Web console: " + webURL,
		fmt.Sprintf("Port pool: %d-%d", s.cfg.AutoPortStart, s.cfg.AutoPortEnd),
		"State file: " + s.cfg.StateFile,
	}

	serverAddr, err := s.publicControlAddress()
	if err != nil {
		lines = append(lines, "", "Client connect: unavailable", "Reason: "+err.Error())
		s.printBox(lines)
		return
	}
	lines = append(lines, "Client connect: "+serverAddr)

	lines = append(lines,
		"",
		"0.1.2 uses per-node credentials.",
		"Open the web console to issue a bootstrap token for each node.",
		"Published mappings and cumulative session stats will be restored after restart.",
	)
	s.printBox(lines)
}

func (s *Server) publicControlAddress() (string, error) {
	host, port, err := splitHostPortLoose(s.cfg.ControlListenAddr)
	if err != nil {
		return "", err
	}

	publicHost := strings.TrimSpace(s.cfg.PublicHost)
	if publicHost == "" {
		publicHost = normalizePublicHost(host)
	}
	if publicHost == "" {
		return "", fmt.Errorf("public_host is empty and control_listen_addr does not contain a public host")
	}

	return net.JoinHostPort(publicHost, port), nil
}

func (s *Server) publicWebURL() string {
	webHost := strings.TrimSpace(s.cfg.PublicHost)
	if webHost == "" {
		webHost = "127.0.0.1"
	}

	_, port, err := splitHostPortLoose(s.cfg.HTTPListenAddr)
	if err != nil {
		return "http://" + webHost
	}
	return "http://" + net.JoinHostPort(webHost, port)
}

func splitHostPortLoose(addr string) (string, string, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "", "", fmt.Errorf("address is empty")
	}

	if strings.HasPrefix(addr, ":") {
		return "", strings.TrimPrefix(addr, ":"), nil
	}

	host, port, err := net.SplitHostPort(addr)
	if err == nil {
		return host, port, nil
	}

	if !strings.Contains(addr, ":") {
		return addr, "", fmt.Errorf("address %q is missing a port", addr)
	}

	lastColon := strings.LastIndex(addr, ":")
	if lastColon <= 0 || lastColon == len(addr)-1 {
		return "", "", fmt.Errorf("address %q is invalid", addr)
	}

	return addr[:lastColon], addr[lastColon+1:], nil
}

func normalizePublicHost(host string) string {
	host = strings.TrimSpace(host)
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		return ""
	default:
		return strings.Trim(host, "[]")
	}
}

func (s *Server) printBox(lines []string) {
	width := 0
	for _, line := range lines {
		if len(line) > width {
			width = len(line)
		}
	}
	if width < 20 {
		width = 20
	}

	border := "+" + strings.Repeat("-", width+2) + "+"
	fmt.Println()
	fmt.Println(border)
	for _, line := range lines {
		padding := width - len(line)
		fmt.Printf("| %s%s |\n", line, strings.Repeat(" ", padding))
	}
	fmt.Println(border)
	fmt.Println()
}
