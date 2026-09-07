//go:build windows

package winclient

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"time"

	"tunnel/internal/common"
)

const (
	tcpOpenLogMarker  = "TCP OPEN "
	tcpCloseLogMarker = "TCP CLOSE "
)

type streamConnectionLogEvent struct {
	ConnectionID  string `json:"connection_id"`
	PresetName    string `json:"preset"`
	RemoteAddr    string `json:"remote_addr"`
	LocalAddr     string `json:"local_addr"`
	DurationMS    int64  `json:"duration_ms,omitempty"`
	BytesToPublic uint64 `json:"bytes_to_public,omitempty"`
	BytesToLocal  uint64 `json:"bytes_to_local,omitempty"`
}

type connectionMonitor struct {
	output       io.Writer
	loginAccount string
	assignment   string
	active       map[string]streamConnectionLogEvent
}

type logFollower struct {
	path     string
	identity os.FileInfo
	offset   int64
	partial  string
}

func runMonitor(output io.Writer) error {
	p, err := systemPaths()
	if err != nil {
		return err
	}

	follower, err := newLogFollower(p.Log)
	if err != nil {
		return fmt.Errorf("打开服务日志监视器失败：%w", err)
	}

	monitor := &connectionMonitor{
		output:       output,
		loginAccount: sshLoginAccount(),
		active:       make(map[string]streamConnectionLogEvent),
	}
	if err := monitor.loadHistory(p.Log); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(output, "警告：无法读取已有连接历史：%v\n", err)
	}
	monitor.printHeader(p)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	pollTicker := time.NewTicker(400 * time.Millisecond)
	defer pollTicker.Stop()
	heartbeatTicker := time.NewTicker(30 * time.Second)
	defer heartbeatTicker.Stop()

	fmt.Fprintln(output, "实时 TCP 监视已启动。关闭此窗口只会隐藏监视器，后台服务仍会继续运行。")
	fmt.Fprintln(output, "按 Ctrl+C 退出监视器。")
	fmt.Fprintln(output)

	lastReadError := ""
	for {
		select {
		case <-ctx.Done():
			fmt.Fprintln(output, "监视器已停止，1CatTunnel 和 OpenSSH 服务仍在运行。")
			return nil
		case <-pollTicker.C:
			lines, readErr := follower.readAvailable()
			if readErr != nil {
				message := readErr.Error()
				if message != lastReadError {
					fmt.Fprintf(output, "[%s] 日志监视警告：%v\n", time.Now().Format("15:04:05"), readErr)
					lastReadError = message
				}
				continue
			}
			lastReadError = ""
			for _, line := range lines {
				monitor.consumeLine(line, true)
			}
		case <-heartbeatTicker.C:
			fmt.Fprintf(output, "[%s] 监视器运行正常 | 活跃 TCP 连接：%d\n", time.Now().Format("15:04:05"), len(monitor.active))
		}
	}
}

func (m *connectionMonitor) printHeader(p paths) {
	fmt.Fprintln(m.output, "1CatTunnel Windows SSH 实时监视器")
	fmt.Fprintln(m.output, "====================================")
	fmt.Fprintf(m.output, "版本：%s\n", common.Version)
	fmt.Fprintf(m.output, "外部 SSH 登录账户：%s\n", m.loginAccount)
	fmt.Fprintln(m.output, "本地 SSH 目标：127.0.0.1:22")

	installed, status, err := serviceState(tunnelServiceName)
	switch {
	case err != nil:
		fmt.Fprintf(m.output, "隧道服务：不可用（%v）\n", err)
	case !installed:
		fmt.Fprintln(m.output, "隧道服务：未安装")
	default:
		fmt.Fprintf(m.output, "隧道服务：%s\n", serviceStateName(status.State))
	}

	if m.assignment == "" {
		m.assignment = latestSSHAssignment(p.Log)
	}
	if m.assignment == "" {
		fmt.Fprintln(m.output, "公网 SSH 映射：正在等待服务器分配")
	} else {
		fmt.Fprintf(m.output, "公网 SSH 映射：%s\n", m.assignment)
		fmt.Fprintf(m.output, "连接命令：%s\n", sshConnectionCommandForAccount(m.assignment, m.loginAccount))
	}

	fmt.Fprintf(m.output, "活跃 TCP 连接：%d\n", len(m.active))
	if len(m.active) > 0 {
		ids := make([]string, 0, len(m.active))
		for id := range m.active {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			event := m.active[id]
			fmt.Fprintf(m.output, "  %s | 映射=%s | 来源=%s | 本地=%s\n", id, fallbackMonitorValue(event.PresetName), fallbackMonitorValue(event.RemoteAddr), fallbackMonitorValue(event.LocalAddr))
		}
	}
	fmt.Fprintln(m.output)
}

func (m *connectionMonitor) loadHistory(logPath string) error {
	var firstErr error
	readAny := false
	for index := serviceLogBackups; index >= 0; index-- {
		path := logPath
		if index > 0 {
			path = fmt.Sprintf("%s.%d", logPath, index)
		}
		file, err := os.Open(path)
		if err != nil {
			if !os.IsNotExist(err) && firstErr == nil {
				firstErr = err
			}
			continue
		}
		readAny = true
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		for scanner.Scan() {
			m.consumeLine(scanner.Text(), false)
		}
		scanErr := scanner.Err()
		closeErr := file.Close()
		if scanErr != nil || closeErr != nil {
			return errors.Join(scanErr, closeErr)
		}
	}
	if !readAny && firstErr != nil {
		return firstErr
	}
	if !readAny {
		return os.ErrNotExist
	}
	return nil
}

func (m *connectionMonitor) consumeLine(line string, live bool) {
	if isTunnelServiceStartLogLine(line) {
		hadConnections := len(m.active) > 0
		clear(m.active)
		if live && hadConnections {
			fmt.Fprintf(m.output, "[%s] 隧道服务已重新启动 | 活跃 TCP 连接：0\n", time.Now().Format("15:04:05"))
		}
	}

	if assignment := sshAssignmentFromLogLine(line); assignment != "" && assignment != m.assignment {
		m.assignment = assignment
		if live {
			fmt.Fprintf(m.output, "[%s] 公网 SSH 映射：%s\n", time.Now().Format("15:04:05"), assignment)
			fmt.Fprintf(m.output, "           连接命令：%s\n", sshConnectionCommandForAccount(assignment, m.loginAccount))
		}
	}

	state, event, ok := parseStreamConnectionLogLine(line)
	if !ok {
		return
	}

	switch state {
	case "OPEN":
		_, existed := m.active[event.ConnectionID]
		m.active[event.ConnectionID] = event
		if live && !existed {
			fmt.Fprintf(
				m.output,
				"[%s] TCP 已连接 | 映射=%s | 来源=%s | 本地=%s | 活跃=%d\n",
				time.Now().Format("15:04:05"),
				fallbackMonitorValue(event.PresetName),
				fallbackMonitorValue(event.RemoteAddr),
				fallbackMonitorValue(event.LocalAddr),
				len(m.active),
			)
		}
	case "CLOSE":
		_, existed := m.active[event.ConnectionID]
		delete(m.active, event.ConnectionID)
		if live && existed {
			fmt.Fprintf(
				m.output,
				"[%s] TCP 已断开 | 映射=%s | 来源=%s | 时长=%s | 发送=%s | 接收=%s | 活跃=%d\n",
				time.Now().Format("15:04:05"),
				fallbackMonitorValue(event.PresetName),
				fallbackMonitorValue(event.RemoteAddr),
				formatDurationMS(event.DurationMS),
				formatByteCount(event.BytesToPublic),
				formatByteCount(event.BytesToLocal),
				len(m.active),
			)
		}
	}
}

func parseStreamConnectionLogLine(line string) (string, streamConnectionLogEvent, bool) {
	for _, candidate := range []struct {
		state  string
		marker string
	}{
		{state: "OPEN", marker: tcpOpenLogMarker},
		{state: "CLOSE", marker: tcpCloseLogMarker},
	} {
		index := strings.Index(line, candidate.marker)
		if index < 0 {
			continue
		}
		payload := strings.TrimSpace(line[index+len(candidate.marker):])
		var event streamConnectionLogEvent
		if json.Unmarshal([]byte(payload), &event) != nil || strings.TrimSpace(event.ConnectionID) == "" {
			return "", streamConnectionLogEvent{}, false
		}
		return candidate.state, event, true
	}
	return "", streamConnectionLogEvent{}, false
}

func newLogFollower(path string) (*logFollower, error) {
	follower := &logFollower{path: path}
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return follower, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	follower.identity = info
	follower.offset = info.Size()
	return follower, nil
}

func (f *logFollower) readAvailable() ([]string, error) {
	file, err := os.Open(f.path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if f.identity == nil || !os.SameFile(f.identity, info) || info.Size() < f.offset {
		f.identity = info
		f.offset = 0
		f.partial = ""
	}
	if _, err := file.Seek(f.offset, io.SeekStart); err != nil {
		return nil, err
	}
	payload, err := io.ReadAll(io.LimitReader(file, serviceLogMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(payload) > serviceLogMaxBytes {
		return nil, fmt.Errorf("两次监视轮询之间服务日志增长超过 %d 字节", serviceLogMaxBytes)
	}
	f.offset += int64(len(payload))
	f.identity = info
	if len(payload) == 0 {
		return nil, nil
	}

	combined := f.partial + string(payload)
	lastNewline := strings.LastIndexByte(combined, '\n')
	if lastNewline < 0 {
		f.partial = combined
		return nil, nil
	}
	f.partial = combined[lastNewline+1:]

	rawLines := strings.Split(combined[:lastNewline], "\n")
	lines := make([]string, 0, len(rawLines))
	for _, line := range rawLines {
		line = strings.TrimSuffix(line, "\r")
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines, nil
}

func fallbackMonitorValue(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "-"
	}
	return value
}

func formatDurationMS(milliseconds int64) string {
	if milliseconds < 0 {
		milliseconds = 0
	}
	return (time.Duration(milliseconds) * time.Millisecond).Round(time.Millisecond).String()
}

func formatByteCount(bytes uint64) string {
	const unit = uint64(1024)
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	value := float64(bytes)
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	for _, suffix := range units {
		value /= float64(unit)
		if value < float64(unit) || suffix == units[len(units)-1] {
			return fmt.Sprintf("%.1f %s", value, suffix)
		}
	}
	return fmt.Sprintf("%d B", bytes)
}
