package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"tunnel/internal/client"
)

func configID(path string) string {
	sum := sha256.Sum256([]byte(path))
	return fmt.Sprintf("%x", sum[:8])
}

func ownedDirectory(path string) error {
	if path == filepath.Dir(path) {
		return nil
	}
	if err := ownedDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return os.Mkdir(path, 0o700)
	}
	if err != nil {
		return err
	}
	st := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || (st.Uid != 0 && st.Uid != uint32(os.Geteuid())) ||
		(info.Mode().Perm()&0o022 != 0 && !(st.Uid == 0 && info.Mode()&os.ModeSticky != 0)) {
		return fmt.Errorf("目录权限不安全：%s", path)
	}
	return nil
}

func writeOwnedFile(path, data string) error {
	if err := ownedDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil {
		st := info.Sys().(*syscall.Stat_t)
		if !info.Mode().IsRegular() || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 {
			return fmt.Errorf("拒绝覆盖非当前账户普通文件：%s", path)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".1cat-write-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := io.WriteString(f, data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func unitArg(s string) string {
	r := strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "%", "%%", "$", "$$")
	return "\"" + r.Replace(s) + "\""
}

func desktopArg(s string) string {
	// Desktop Exec has its own quoting rules and a second backslash parse layer.
	r := strings.NewReplacer("\\", "\\\\\\\\", "\"", "\\\"", "`", "\\`", "$", "\\$", "%", "%%")
	return "\"" + r.Replace(s) + "\""
}

func installDesktopEntry(config string) error {
	root, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if strings.ContainsAny(exe+config, "\n\r") {
		return fmt.Errorf("路径不能包含换行")
	}
	entry := "[Desktop Entry]\nType=Application\nName=1CatTunnel 状态与日志\n" +
		"Comment=登录桌面后显示客户端实时状态\nTerminal=false\n" +
		"Exec=" + desktopArg(exe) + " -desktop-watch -config " + desktopArg(config) + "\n" +
		"X-GNOME-Autostart-enabled=true\nX-GNOME-Autostart-Delay=3\n"
	return writeOwnedFile(filepath.Join(root, "autostart", "1cattunnel-"+configID(config)+".desktop"), entry)
}

func registerAutostart(ctx context.Context, config string) error {
	if err := installDesktopEntry(config); err != nil {
		return err
	}
	dir, _ := serviceMetadata(config)
	if dir != "" {
		helper := filepath.Join(dir, "register-service.sh")
		info, err := os.Lstat(helper)
		if err != nil {
			return err
		}
		st := info.Sys().(*syscall.Stat_t)
		if !info.Mode().IsRegular() || st.Uid != 0 || info.Mode().Perm()&0o022 != 0 {
			return fmt.Errorf("服务登记工具权限无效")
		}
		args := []string{helper, "--yes", "--register-only", config}
		if os.Geteuid() != 0 {
			err = runInteractive(ctx, "sudo", append([]string{"sh"}, args...)...)
		} else {
			err = runInteractive(ctx, "sh", args...)
		}
		if err != nil {
			return err
		}
		fmt.Println("已登记系统开机自启；当前仍在本窗口运行，重启电脑后由后台服务启动。")
		return nil
	}
	root, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	unit := "1cat-tunnel-client-" + configID(config) + ".service"
	data := "[Unit]\nDescription=1Cat Tunnel Client\nAfter=network-online.target\n" +
		"[Service]\nExecStart=" + unitArg(exe) + " -service-mode -config " + unitArg(config) + "\n" +
		"Restart=on-failure\nRestartSec=5\nRestartPreventExitStatus=75\nUMask=0077\n" +
		"[Install]\nWantedBy=default.target\n"
	if err := writeOwnedFile(filepath.Join(root, "systemd", "user", unit), data); err != nil {
		return err
	}
	if err := runInteractive(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
		return err
	}
	if err := runInteractive(ctx, "systemctl", "--user", "enable", unit); err != nil {
		return err
	}
	fmt.Println("已登记用户服务：登录此 Linux 用户后自动启动。系统启动即运行请使用系统级安装。")
	return nil
}

func terminalCommand(exe, config string) (string, []string) {
	for _, candidate := range []struct {
		name string
		args []string
	}{
		{"gnome-terminal", []string{"--title=1CatTunnel 状态与日志", "--"}},
		{"konsole", []string{"--separate", "-e"}},
		{"xfce4-terminal", []string{"--disable-server", "--title=1CatTunnel 状态与日志", "-x"}},
		{"x-terminal-emulator", []string{"-T", "1CatTunnel 状态与日志", "-e"}},
		{"xterm", []string{"-T", "1CatTunnel 状态与日志", "-e"}},
		{"mate-terminal", []string{"--disable-factory", "-x"}},
	} {
		if command, err := exec.LookPath(candidate.name); err == nil {
			return command, append(candidate.args, exe, "-monitor", "-config", config)
		}
	}
	return "", nil
}

func openDesktopMonitor(config string, explicit bool) error {
	if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		if explicit {
			return fmt.Errorf("当前没有图形桌面会话；请执行 1cattunnel monitor 在终端观察")
		}
		return nil
	}
	launch, err := lockFile(context.Background(), config+".popup.lock", false)
	if errors.Is(err, unix.EWOULDBLOCK) {
		return nil
	}
	if err != nil {
		return err
	}
	defer launch.Close()
	probe, err := lockFile(context.Background(), config+".monitor.lock", false)
	if errors.Is(err, unix.EWOULDBLOCK) {
		return nil
	}
	if err != nil {
		return err
	}
	probe.Close()
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	command, args := terminalCommand(exe, config)
	if command == "" {
		return fmt.Errorf("未找到桌面终端，可执行 1cattunnel monitor 查看状态")
	}
	cmd := exec.Command(command, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	for range 40 {
		probe, err := lockFile(context.Background(), config+".monitor.lock", false)
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil
		}
		if err != nil {
			return err
		}
		probe.Close()
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("桌面终端没有启动观察进程，请执行 1cattunnel monitor")
}

func startDesktopObserver(config string) {
	if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		return
	}
	probe, err := lockFile(context.Background(), config+".desktop.lock", false)
	if err != nil {
		return
	}
	probe.Close()
	exe, err := os.Executable()
	if err != nil {
		return
	}
	cmd := exec.Command(exe, "-desktop-watch", "-config", config)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if cmd.Start() == nil {
		go cmd.Wait()
	}
}

func watchDesktop(ctx context.Context, config string) error {
	if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		return nil
	}
	lock, err := lockFile(ctx, config+".desktop.lock", false)
	if errors.Is(err, unix.EWOULDBLOCK) {
		return nil
	}
	if err != nil {
		return err
	}
	defer lock.Close()
	last := ""
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		if f, err := openRuntimeFile(config + ".instance.lock"); err == nil {
			if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); errors.Is(err, unix.EWOULDBLOCK) {
				data, _ := io.ReadAll(io.LimitReader(f, 32))
				pid := strings.TrimSpace(string(data))
				if pid != "" && pid != last {
					if openDesktopMonitor(config, false) == nil {
						last = pid
					}
				}
			}
			f.Close()
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func monitorLinux(ctx context.Context, config string) error {
	lock, err := lockFile(ctx, config+".monitor.lock", false)
	if errors.Is(err, unix.EWOULDBLOCK) {
		return nil
	}
	if err != nil {
		return err
	}
	defer lock.Close()
	fmt.Println("1CatTunnel 状态与日志 | 关闭此窗口不会停止客户端 | Ctrl+C 关闭观察")
	fmt.Println("配置：", config)
	lastStatus := ""
	var offset int64 = -1
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		cfg, err := client.LoadManagedConfig(config)
		text := "客户端未运行，等待启动……"
		if err == nil {
			data, queryErr := client.QueryManagement(cfg, "/api/status", true)
			if queryErr == nil {
				var status struct {
					Version  string `json:"version"`
					Status   string `json:"status"`
					Server   string `json:"server_addr"`
					Node     string `json:"node_name"`
					Mappings []struct {
						Protocol string `json:"protocol"`
						Local    string `json:"local_addr"`
						Public   string `json:"public_target"`
						Status   string `json:"status"`
					} `json:"mappings"`
				}
				if json.Unmarshal(data, &status) == nil {
					text = fmt.Sprintf("%s | %s | 节点=%s | 服务器=%s", status.Version, status.Status, status.Node, status.Server)
					for _, mapping := range status.Mappings {
						text += fmt.Sprintf("\n  %s  %s -> %s  %s", mapping.Protocol, mapping.Public, mapping.Local, mapping.Status)
					}
				}
			}
		}
		if text != lastStatus {
			fmt.Println(time.Now().Format("15:04:05"), text)
			lastStatus = text
		}
		if f, err := openRuntimeFile(config + ".log"); err == nil {
			if info, err := f.Stat(); err == nil {
				if offset < 0 {
					offset = max(0, info.Size()-16384)
				}
				if offset > info.Size() {
					offset = 0
				}
				if _, err := f.Seek(offset, io.SeekStart); err == nil {
					count, _ := io.Copy(os.Stdout, io.LimitReader(f, 65536))
					offset += count
				}
			}
			f.Close()
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
