// Package daemon ties the storage, HTTP API, and enforcement loops together
// into a single long-running process.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/ketan0/attend/internal/appmon"
	"github.com/ketan0/attend/internal/hosts"
	"github.com/ketan0/attend/internal/rules"
	"github.com/ketan0/attend/internal/server"
	"github.com/ketan0/attend/internal/store"
)

// Config controls daemon startup.
type Config struct {
	Addr      string        // e.g. "127.0.0.1:7723"
	StorePath string        // e.g. "~/.config/attend/rules.json"
	HostsPath string        // typically "/etc/hosts"
	TickEvery time.Duration // enforcement tick interval; default 5s
	Version   string
	Logger    *log.Logger
}

// Daemon is a constructed (but not running) daemon.
type Daemon struct {
	cfg    Config
	store  *store.FileStore
	hosts  *hosts.Manager
	mon    *appmon.Monitor
	server *server.Server
	httpd  *http.Server
	// poke wakes the run loop to re-enforce immediately. Buffered so the
	// store's change hook never blocks; multiple pokes coalesce into one
	// extra enforcement pass.
	poke chan struct{}
}

// New wires up dependencies. It opens the store and prepares (but does not
// start) the HTTP server.
func New(cfg Config) (*Daemon, error) {
	if cfg.Addr == "" {
		cfg.Addr = "127.0.0.1:7723"
	}
	if cfg.StorePath == "" {
		return nil, errors.New("StorePath is required")
	}
	if cfg.HostsPath == "" {
		cfg.HostsPath = "/etc/hosts"
	}
	if cfg.TickEvery == 0 {
		cfg.TickEvery = 5 * time.Second
	}
	if cfg.Logger == nil {
		cfg.Logger = log.Default()
	}

	st, err := store.Open(cfg.StorePath)
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}

	hm := hosts.New(hosts.OSFS{}, cfg.HostsPath)
	mon := &appmon.Monitor{Lister: appmon.OSALister{}, Quitter: appmon.OSAQuitter{}}

	srv := server.New(st)
	srv.Version = cfg.Version

	httpd := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	d := &Daemon{
		cfg: cfg, store: st, hosts: hm, mon: mon, server: srv, httpd: httpd,
		poke: make(chan struct{}, 1),
	}
	st.SetChangeHook(d.notifyPoke)
	return d, nil
}

// notifyPoke nudges Run to re-enforce immediately. Non-blocking.
func (d *Daemon) notifyPoke() {
	select {
	case d.poke <- struct{}{}:
	default:
	}
}

// Run blocks, serving HTTP and running the enforcement tick loop until ctx is
// cancelled.
func (d *Daemon) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", d.cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", d.cfg.Addr, err)
	}
	d.cfg.Logger.Printf("attendd listening on %s", d.cfg.Addr)

	httpDone := make(chan error, 1)
	go func() { httpDone <- d.httpd.Serve(ln) }()

	tick := time.NewTicker(d.cfg.TickEvery)
	defer tick.Stop()
	d.enforce()

	for {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = d.httpd.Shutdown(shutdownCtx)
			<-httpDone
			return nil
		case <-tick.C:
			d.enforce()
		case <-d.poke:
			d.enforce()
		case err := <-httpDone:
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return err
		}
	}
}

// enforce applies all currently-active rules to the world. Errors are logged
// but do not stop the loop — a transient failure should not take the daemon
// down.
func (d *Daemon) enforce() {
	now := time.Now()
	settings := d.store.Settings()
	if settings.IsPaused(now) {
		// While paused, remove any active blocks so the user has free use.
		if err := d.hosts.Apply(nil); err != nil {
			d.cfg.Logger.Printf("hosts.Apply(nil) during pause: %v", err)
		}
		return
	}

	// SystemBlocks computes what the OS layer should be enforcing right now,
	// after honoring allow rules that narrow or override block rules.
	domains, apps := rules.SystemBlocks(d.store.List(), now)

	if err := d.hosts.Apply(domains); err != nil {
		d.cfg.Logger.Printf("hosts.Apply: %v", err)
	}
	if results, err := d.mon.Sweep(apps); err != nil {
		d.cfg.Logger.Printf("appmon.Sweep: %v", err)
	} else {
		for _, r := range results {
			if r.QuitErr != nil {
				d.cfg.Logger.Printf("quit %q: %v", r.App, r.QuitErr)
			}
		}
	}
}
