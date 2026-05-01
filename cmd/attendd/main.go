// attendd is the attend background daemon.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/ketan0/attend/internal/daemon"
)

const version = "0.1.0"

func main() {
	addr := flag.String("addr", "127.0.0.1:7723", "HTTP listen address")
	storePath := flag.String("store", defaultStorePath(), "rule store path")
	hostsPath := flag.String("hosts", "/etc/hosts", "hosts file path")
	frictionApp := flag.String("friction-app", "", "path to AttendFriction binary (empty disables native friction)")
	flag.Parse()

	logger := log.New(os.Stderr, "attendd ", log.LstdFlags|log.Lmicroseconds)

	d, err := daemon.New(daemon.Config{
		Addr:            *addr,
		StorePath:       *storePath,
		HostsPath:       *hostsPath,
		FrictionAppPath: *frictionApp,
		Version:         version,
		Logger:          logger,
	})
	if err != nil {
		logger.Fatalf("daemon init: %v", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := d.Run(ctx); err != nil {
		logger.Fatalf("daemon run: %v", err)
	}
}

func defaultStorePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "rules.json"
	}
	return filepath.Join(home, ".config", "attend", "rules.json")
}
