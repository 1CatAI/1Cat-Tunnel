package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

var errAlreadyRunning = errors.New("同一配置已有前台客户端，后台服务不重复启动")

func openRuntimeFile(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CREAT|unix.O_APPEND|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0o600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		f.Close()
		return nil, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
		f.Close()
		return nil, fmt.Errorf("运行文件必须是当前用户独占的普通文件：%s", path)
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func lockFile(ctx context.Context, path string, wait bool) (*os.File, error) {
	f, err := openRuntimeFile(path)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) || !wait || time.Now().After(deadline) {
			f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func processUsesConfig(pid int, config string) bool {
	if pid == os.Getpid() || pid <= 1 {
		return false
	}
	base := filepath.Join("/proc", strconv.Itoa(pid))
	info, err := os.Stat(base)
	if err != nil || info.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) {
		return false
	}
	name, err := os.Readlink(filepath.Join(base, "exe"))
	if err != nil {
		return false
	}
	name = filepath.Base(strings.TrimSuffix(name, " (deleted)"))
	switch name {
	case "tunnel-client", "1cat-tunnel-client", "tunnel-client-linux-amd64", "1cattunnel":
	default:
		return false
	}
	data, err := os.ReadFile(filepath.Join(base, "cmdline"))
	if err != nil {
		return false
	}
	args := strings.Split(strings.TrimRight(string(data), "\x00"), "\x00")
	path := ""
	for i, arg := range args[1:] {
		// Observers must never be killed by a new foreground client.
		switch strings.SplitN(arg, "=", 2)[0] {
		case "monitor", "logs", "desktop", "panel", "status", "-monitor", "--monitor", "-desktop", "--desktop", "-desktop-watch", "--desktop-watch", "-panel", "--panel", "-status", "--status", "-initialize-config", "-validate-config":
			return false
		}
		if (arg == "-config" || arg == "--config") && i+2 < len(args) {
			path = args[i+2]
		} else if strings.HasPrefix(arg, "-config=") || strings.HasPrefix(arg, "--config=") {
			path = strings.SplitN(arg, "=", 2)[1]
		}
	}
	if path == "" {
		env, err := os.ReadFile(filepath.Join(base, "environ"))
		if err != nil {
			return false
		}
		root, home := "", ""
		for _, value := range strings.Split(string(env), "\x00") {
			if strings.HasPrefix(value, "XDG_CONFIG_HOME=") {
				root = strings.TrimPrefix(value, "XDG_CONFIG_HOME=")
			}
			if strings.HasPrefix(value, "HOME=") {
				home = strings.TrimPrefix(value, "HOME=")
			}
		}
		if root == "" {
			root = filepath.Join(home, ".config")
		}
		if !filepath.IsAbs(root) {
			return false
		}
		path = filepath.Join(root, "1cat-tunnel", "client-linux.json")
	}
	if !filepath.IsAbs(path) {
		cwd, err := os.Readlink(filepath.Join(base, "cwd"))
		if err != nil {
			return false
		}
		path = filepath.Join(cwd, path)
	}
	return filepath.Clean(path) == filepath.Clean(config)
}

func terminateMatching(ctx context.Context, config string) error {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		// FindProcess pins process identity using pidfd where supported by Go.
		process, err := os.FindProcess(pid)
		if err != nil {
			continue
		}
		if !processUsesConfig(pid, config) {
			process.Release()
			continue
		}
		fmt.Printf("正在接管旧客户端，PID=%d\n", pid)
		err = process.Signal(syscall.SIGTERM)
		if err != nil && !errors.Is(err, os.ErrProcessDone) {
			process.Release()
			return err
		}
		deadline := time.Now().Add(5 * time.Second)
		for processUsesConfig(pid, config) && time.Now().Before(deadline) && ctx.Err() == nil {
			time.Sleep(100 * time.Millisecond)
		}
		if processUsesConfig(pid, config) {
			if err := process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				process.Release()
				return err
			}
		}
		process.Release()
	}
	return ctx.Err()
}

func acquireInstance(ctx context.Context, config string, replace bool) (func(), error) {
	// Serialize takeovers separately from the lifetime lock. Never unlink lock files.
	startup, err := lockFile(ctx, config+".start.lock", true)
	if err != nil {
		return nil, err
	}
	defer startup.Close()
	if replace {
		if err := terminateMatching(ctx, config); err != nil {
			return nil, err
		}
	}
	instance, err := lockFile(ctx, config+".instance.lock", replace)
	if errors.Is(err, unix.EWOULDBLOCK) {
		return nil, errAlreadyRunning
	}
	if err != nil {
		return nil, err
	}
	if err := instance.Truncate(0); err != nil {
		instance.Close()
		return nil, err
	}
	if _, err := fmt.Fprintln(instance, os.Getpid()); err != nil {
		instance.Close()
		return nil, err
	}
	return func() { instance.Close() }, nil
}

func serviceMetadata(config string) (string, string) {
	exe, err := os.Executable()
	if err != nil {
		return "", ""
	}
	dir := filepath.Dir(exe)
	data, err := os.ReadFile(filepath.Join(dir, "client-config-path"))
	if err != nil || strings.TrimSpace(string(data)) != config {
		return "", ""
	}
	unit := "1cat-tunnel-client.service"
	if data, err := os.ReadFile(filepath.Join(dir, "service-unit")); err == nil {
		unit = strings.TrimSpace(string(data))
	}
	if strings.ContainsAny(unit, "/\n\r ") || !strings.HasSuffix(unit, ".service") || strings.HasPrefix(unit, "-") {
		return "", ""
	}
	return dir, unit
}

func runInteractive(ctx context.Context, command string, args ...string) error {
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

func stopManagedService(ctx context.Context, config string) error {
	_, unit := serviceMetadata(config)
	if unit == "" {
		unit = "1cat-tunnel-client.service"
	}
	for _, spec := range []struct {
		unit string
		user bool
	}{{unit, false}, {unit, true}, {"1cat-tunnel-client-" + configID(config) + ".service", true}} {
		unit, user := spec.unit, spec.user
		args := []string{"show", unit, "-p", "MainPID", "--value"}
		if user {
			args = append([]string{"--user"}, args...)
		}
		data, err := exec.CommandContext(ctx, "systemctl", args...).Output()
		if err != nil {
			continue
		}
		pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
		if !processUsesConfig(pid, config) {
			continue
		}
		fmt.Println("停止此配置的后台服务，切换为当前窗口运行；开机自启设置保留。")
		args = []string{"stop", unit}
		if user {
			args = append([]string{"--user"}, args...)
		}
		if !user && os.Geteuid() != 0 {
			return runInteractive(ctx, "sudo", append([]string{"systemctl"}, args...)...)
		}
		return runInteractive(ctx, "systemctl", args...)
	}
	return nil
}
