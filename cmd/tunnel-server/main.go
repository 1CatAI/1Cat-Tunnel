package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	"tunnel/internal/common"
	"tunnel/internal/launcher"
	"tunnel/internal/server"
)

func main() {
	configPath := flag.String("config", "", "path to server config")
	pauseOnExit := flag.Bool("pause-on-exit", runtime.GOOS == "windows", "pause before exit on Windows when startup fails")
	flag.Parse()

	logCleanup, logPath, err := launcher.SetupLogging("tunnel-server.log")
	if err == nil {
		defer logCleanup()
		log.Printf("logging to %s", logPath)
	} else {
		launcher.WarnIfLoggingSetupFails(err)
	}
	log.Printf("starting %s", common.Version)

	resolvedConfigPath, err := launcher.ResolveConfigPath(*configPath, []string{"server.json"})
	if err != nil {
		launcher.FatalfWithPause(*pauseOnExit, "resolve server config: %v", err)
	}
	log.Printf("using config %s", resolvedConfigPath)

	cfg, err := server.LoadConfig(resolvedConfigPath)
	if err != nil {
		launcher.FatalfWithPause(*pauseOnExit, "load server config: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := server.Run(ctx, cfg); err != nil {
		launcher.FatalfWithPause(*pauseOnExit, "server stopped with error: %v", err)
	}
}
