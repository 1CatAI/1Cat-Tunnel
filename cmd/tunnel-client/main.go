package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"

	"tunnel/internal/client"
	"tunnel/internal/common"
	"tunnel/internal/launcher"
)

func main() {
	configPath := flag.String("config", "", "path to client config")
	pauseOnExit := flag.Bool("pause-on-exit", runtime.GOOS == "windows", "pause before exit on Windows when startup fails")
	flag.Parse()

	logCleanup, logPath, err := launcher.SetupLogging("tunnel-client.log")
	if err == nil {
		defer logCleanup()
		log.Printf("logging to %s", logPath)
	} else {
		launcher.WarnIfLoggingSetupFails(err)
	}
	log.Printf("starting %s", common.Version)

	resolvedConfigPath, err := launcher.ResolveConfigPath(*configPath, []string{
		"client.json",
		"client-" + runtime.GOOS + ".json",
	})

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
	if err != nil && interactiveSetup {
		log.Printf("client config needs setup: %v", err)
		cfg, err = client.BootstrapConfig(resolvedConfigPath)
	}
	if err != nil {
		launcher.FatalfWithPause(*pauseOnExit, "load client config: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := client.Run(ctx, cfg); err != nil {
		launcher.FatalfWithPause(*pauseOnExit, "client stopped with error: %v", err)
	}
}

func defaultClientConfigPath() string {
	exePath, err := os.Executable()
	if err != nil {
		return "client-" + runtime.GOOS + ".json"
	}
	return filepath.Join(filepath.Dir(exePath), "client-"+runtime.GOOS+".json")
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
