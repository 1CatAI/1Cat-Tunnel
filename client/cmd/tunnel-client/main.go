package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"tunnel/internal/client"
	"tunnel/internal/common"
	"tunnel/internal/launcher"
)

func main() {
	configPath := flag.String("config", "", "path to client config")
	managed := flag.Bool("managed", true, "run continuously with the local management panel")
	setup := flag.Bool("setup", false, "configure using the terminal wizard instead of the Web panel")
	version := flag.Bool("version", false, "print the client version")
	initialize := flag.Bool("initialize-config", false, "create private configuration without connecting")
	panel := flag.Bool("panel", false, "print the current user's management login link")
	status := flag.Bool("status", false, "query status with owner authentication")
	serviceMode := flag.Bool("service-mode", false, "never print private login links to service logs")
	pauseOnExit := flag.Bool("pause-on-exit", runtime.GOOS == "windows", "pause before exit on Windows when startup fails")
	flag.Parse()
	if *version {
		fmt.Println(common.Version)
		return
	}
	if *setup {
		*managed = false
	}
	if *initialize || *panel || *status {
		resolved, err := resolveClientConfigPath(*configPath, true)
		if err == nil && *initialize {
			err = client.InitializeManagedConfig(resolved)
		} else if err == nil {
			var cfg client.Config
			cfg, err = client.LoadManagedConfig(resolved)
			if err == nil && *panel {
				var address string
				address, err = client.ManagementURL(cfg)
				if err == nil {
					fmt.Println(address)
				}
			} else if err == nil {
				var data []byte
				data, err = client.QueryManagement(cfg, "/api/status", true)
				if err == nil {
					fmt.Println(string(data))
				}
			}
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	logCleanup, logPath, err := launcher.SetupLogging("tunnel-client.log")
	if err == nil {
		defer logCleanup()
		log.Printf("logging to %s", logPath)
	} else {
		launcher.WarnIfLoggingSetupFails(err)
	}
	log.Printf("starting %s", common.Version)

	resolvedConfigPath, err := resolveClientConfigPath(*configPath, *managed)
	if *managed {
		if err != nil {
			launcher.FatalfWithPause(*pauseOnExit, "resolve managed client config: %v", err)
		}
		log.Printf("using managed config %s", resolvedConfigPath)
		cfg, loadErr := client.LoadManagedConfig(resolvedConfigPath)
		if loadErr != nil {
			launcher.FatalfWithPause(*pauseOnExit, "load managed client config: %v", loadErr)
		}
		if !*serviceMode {
			if health, healthErr := client.QueryManagement(cfg, "/health", false); healthErr == nil {
				var info struct {
					Version string `json:"version"`
				}
				_ = json.Unmarshal(health, &info)
				fmt.Println("本机管理端口已被运行中的客户端占用：", info.Version)
				address, panelErr := client.ManagementURL(cfg)
				if panelErr != nil {
					fmt.Fprintln(os.Stderr, "请以服务所属账户操作；旧服务需显式升级后重启。", panelErr)
					os.Exit(1)
				}
				fmt.Println(address)
				return
			}
		}
		fmt.Printf("1CatTunnel 本机管理页面：http://%s/\n", cfg.WebListenAddr)
		fmt.Println("请在页面中填写接入凭据、托管服务器和本机映射；程序会持续运行。")
		if !*serviceMode && shouldUseInteractiveSetup() {
			go func() {
				for range 40 {
					time.Sleep(100 * time.Millisecond)
					if address, err := client.ManagementURL(cfg); err == nil {
						fmt.Println("本机专用登录链接（请勿分享）：", address)
						return
					}
				}
			}()
		} else {
			fmt.Println("管理登录：在同一系统账户下执行 1cattunnel panel。")
		}
		runClient(*pauseOnExit, cfg, true)
		return
	}

	interactiveSetup := shouldUseInteractiveSetup()
	if err != nil && interactiveSetup {
		if *configPath != "" {
			resolvedConfigPath = *configPath
		} else {
			resolvedConfigPath = defaultClientConfigPath()
		}
		log.Printf("client config not ready, starting setup wizard at %s", resolvedConfigPath)
	} else if err != nil {
		launcher.FatalfWithPause(*pauseOnExit, "resolve client config: %v", err)
	}

	log.Printf("using config %s", resolvedConfigPath)

	cfg, err := client.LoadConfig(resolvedConfigPath)
	if *setup {
		cfg, err = client.BootstrapConfig(resolvedConfigPath)
		if err != nil {
			launcher.FatalfWithPause(*pauseOnExit, "configure client: %v", err)
		}
		runClient(*pauseOnExit, cfg, false)
		return
	}
	if err != nil && interactiveSetup {
		log.Printf("client config needs setup: %v", err)
		cfg, err = client.BootstrapConfig(resolvedConfigPath)
	}
	if err != nil {
		launcher.FatalfWithPause(*pauseOnExit, "load client config: %v", err)
	}

	runClient(*pauseOnExit, cfg, false)
}

func resolveClientConfigPath(requested string, managed bool) (string, error) {
	if managed {
		if requested != "" {
			return filepath.Abs(requested)
		}
		if resolved, err := launcher.ResolveConfigPath("", []string{
			"client.json",
			"client-" + runtime.GOOS + ".json",
		}); err == nil {
			return resolved, nil
		}
		return defaultManagedClientConfigPath()
	}
	return launcher.ResolveConfigPath(requested, []string{
		"client.json",
		"client-" + runtime.GOOS + ".json",
	})
}

func runClient(pauseOnExit bool, cfg client.Config, managed bool) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	if managed {
		err = client.RunManaged(ctx, cfg)
	} else {
		err = client.Run(ctx, cfg)
	}
	if err != nil {
		launcher.FatalfWithPause(pauseOnExit, "client stopped with error: %v", err)
	}
}

func defaultClientConfigPath() string {
	exePath, err := os.Executable()
	if err != nil {
		return "client-" + runtime.GOOS + ".json"
	}
	return filepath.Join(filepath.Dir(exePath), "client-"+runtime.GOOS+".json")
}

func defaultManagedClientConfigPath() (string, error) {
	configRoot, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(configRoot, "1cat-tunnel", "client-"+runtime.GOOS+".json"), nil
}

func shouldUseInteractiveSetup() bool {
	stdinInfo, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	stdoutInfo, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (stdinInfo.Mode()&os.ModeCharDevice) != 0 && (stdoutInfo.Mode()&os.ModeCharDevice) != 0
}
