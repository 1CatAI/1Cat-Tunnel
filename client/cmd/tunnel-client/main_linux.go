package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"tunnel/internal/client"
)

func main() {
	if err := linuxMain(); err != nil {
		fmt.Fprintln(os.Stderr, "1cattunnel:", err)
		if err == errAlreadyRunning {
			os.Exit(75)
		}
		os.Exit(1)
	}
}

func linuxMain() error {
	// Subcommands are aliases of flags, so explicit -config works everywhere.
	if len(os.Args) > 1 {
		aliases := map[string]string{"setup": "-setup", "config": "-setup", "status": "-status", "panel": "-panel", "monitor": "-monitor", "logs": "-monitor", "desktop": "-desktop"}
		if alias, ok := aliases[os.Args[1]]; ok {
			os.Args[1] = alias
		}
	}
	path := flag.String("config", "", "配置文件路径")
	setup := flag.Bool("setup", false, "终端配置，保存后登记开机自启并在当前窗口运行")
	version := flag.Bool("version", false, "显示版本")
	initialize := flag.Bool("initialize-config", false, "只初始化配置，不连接、不创建服务")
	validate := flag.Bool("validate-config", false, "只校验配置是否已经填写完整")
	panel := flag.Bool("panel", false, "显示当前账户的本机管理链接")
	status := flag.Bool("status", false, "显示实时连接和映射状态 JSON")
	monitor := flag.Bool("monitor", false, "只观察状态与日志，不重启客户端")
	desktop := flag.Bool("desktop", false, "在已登录桌面打开状态日志窗口")
	desktopWatch := flag.Bool("desktop-watch", false, "桌面会话内监听客户端启动和重启")
	service := flag.Bool("service-mode", false, "由后台服务运行")
	flag.Bool("managed", true, "保留本机管理面板")
	flag.Bool("pause-on-exit", false, "兼容旧启动脚本")
	flag.Parse()
	if flag.NArg() != 0 {
		return fmt.Errorf("无法识别参数 %q；使用 1cattunnel -h 查看说明", flag.Arg(0))
	}
	if *version {
		fmt.Println(client.Version)
		return nil
	}
	var err error
	if *path == "" {
		root, e := os.UserConfigDir()
		if e != nil {
			return e
		}
		*path = filepath.Join(root, "1cat-tunnel", "client-linux.json")
	}
	*path, err = filepath.Abs(*path)
	if err != nil {
		return err
	}
	if *initialize {
		return client.InitializeManagedConfig(*path)
	}
	if *validate {
		_, err := client.LoadConfig(*path)
		return err
	}
	cfg, err := client.LoadManagedConfig(*path)
	if err != nil {
		return err
	}
	if *panel || *status {
		if *panel {
			address, err := client.ManagementURL(cfg)
			if err == nil {
				fmt.Println(address)
			}
			return err
		}
		data, err := client.QueryManagement(cfg, "/api/status", true)
		if err == nil {
			fmt.Println(string(data))
		}
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer cancel()
	if *monitor {
		return monitorLinux(ctx, *path)
	}
	if *desktop {
		return openDesktopMonitor(*path, true)
	}
	if *desktopWatch {
		return watchDesktop(ctx, *path)
	}
	if err := client.InitializeManagedConfig(*path); err != nil {
		return err
	}
	if !*service {
		if err := stopManagedService(ctx, *path); err != nil {
			return err
		}
	}
	unlock, err := acquireInstance(ctx, *path, !*service)
	if err != nil {
		return err
	}
	defer unlock()
	// Reload after takeover: the old process may have just persisted its token.
	cfg, err = client.LoadManagedConfig(*path)
	if err != nil {
		return err
	}
	if *setup {
		cfg, err = client.BootstrapConfig(*path)
		if err != nil {
			return err
		}
		if err := registerAutostart(ctx, *path); err != nil {
			fmt.Fprintln(os.Stderr, "开机自启登记未完成：", err)
			fmt.Fprintln(os.Stderr, "配置已保存，继续在当前窗口运行；以后可重新执行 1cattunnel -setup 登记。")
		}
	}
	file, err := openRuntimeFile(*path + ".log")
	if err != nil {
		return err
	}
	defer file.Close()
	// Bounded per-config logs remain writable under systemd's ProtectHome policy.
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Size() > 8<<20 {
		if err := file.Truncate(0); err != nil {
			return err
		}
	}
	log.SetOutput(io.MultiWriter(os.Stdout, &boundedRuntimeLog{file: file, size: min(info.Size(), 8<<20)}))
	defer log.SetOutput(os.Stderr)
	log.Printf("%s 已启动，PID=%d，配置=%s", client.Version, os.Getpid(), *path)
	log.Printf("服务器：%s；本机面板：http://%s/", cfg.ServerAddr, cfg.WebListenAddr)
	log.Print("连接成功后显示实际服务器 IP、端口及公网映射；TCP 建立/关闭和流量会持续记录。")
	if !*service {
		fmt.Println("当前窗口持续运行，Ctrl+C 停止。再次执行 1cattunnel 会接管此配置的旧进程。")
		startDesktopObserver(*path)
		if err := openDesktopMonitor(*path, false); err != nil {
			log.Printf("桌面状态窗口：%v；当前终端继续显示日志", err)
		}
	}
	return client.RunManaged(ctx, cfg)
}

type boundedRuntimeLog struct {
	file *os.File
	size int64
}

func (w *boundedRuntimeLog) Write(data []byte) (int, error) {
	if w.size+int64(len(data)) > 8<<20 {
		if err := w.file.Truncate(0); err != nil {
			return 0, err
		}
		w.size = 0
	}
	n, err := w.file.Write(data)
	w.size += int64(n)
	return n, err
}
