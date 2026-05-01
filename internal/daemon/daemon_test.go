package daemon

import (
	"context"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ketan0/attend/internal/appmon"
	"github.com/ketan0/attend/internal/hosts"
	"github.com/ketan0/attend/internal/rules"
	"github.com/ketan0/attend/internal/store"
)

// memHostsFS implements hosts.FS using an in-memory map.
type memHostsFS struct {
	files map[string][]byte
}

func newMemHostsFS() *memHostsFS { return &memHostsFS{files: map[string][]byte{}} }

func (m *memHostsFS) ReadFile(p string) ([]byte, error) {
	if b, ok := m.files[p]; ok {
		return append([]byte(nil), b...), nil
	}
	return nil, os.ErrNotExist
}

func (m *memHostsFS) WriteFile(p string, data []byte, _ os.FileMode) error {
	m.files[p] = append([]byte(nil), data...)
	return nil
}

func TestDaemonEnforceWiresDomainsAndApps(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "rules.json"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}

	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	mkRule := func(id string, action rules.Action, kind rules.TargetKind, value string) rules.Rule {
		r := rules.Rule{
			ID:        id,
			Action:    action,
			Target:    rules.Target{Kind: kind, Value: value},
			Schedule:  rules.Schedule{Kind: rules.ScheduleAlways},
			CreatedAt: now,
			UpdatedAt: now,
		}
		if action == rules.ActionFriction {
			r.Friction = &rules.FrictionConfig{Level: rules.FrictionTimer, TimerSeconds: 10}
		}
		return r
	}
	for _, r := range []rules.Rule{
		mkRule("r1", rules.ActionBlock, rules.TargetDomain, "twitter.com"),
		mkRule("r2", rules.ActionBlock, rules.TargetApp, "Slack"),
		mkRule("r3", rules.ActionFriction, rules.TargetDomain, "reddit.com"),
	} {
		if err := st.Put(r); err != nil {
			t.Fatalf("Put %s: %v", r.ID, err)
		}
	}

	hm := hosts.New(newMemHostsFS(), "/etc/hosts")
	lister := &appmon.FakeLister{Apps: []string{"Slack", "Safari", "Mail"}}
	quitter := &appmon.FakeQuitter{}
	mon := &appmon.Monitor{Lister: lister, Quitter: quitter}

	d := &Daemon{
		cfg:   Config{Logger: log.New(io.Discard, "", 0), TickEvery: time.Hour},
		store: st,
		hosts: hm,
		mon:   mon,
		poke:  make(chan struct{}, 1),
	}

	d.enforce()

	doms, err := hm.CurrentDomains()
	if err != nil {
		t.Fatalf("CurrentDomains: %v", err)
	}
	if len(doms) != 1 || doms[0] != "twitter.com" {
		t.Errorf("CurrentDomains = %v, want [twitter.com] (friction rule should not block)", doms)
	}
	quits := quitter.QuitsSnapshot()
	if len(quits) != 1 || quits[0] != "Slack" {
		t.Errorf("Quits = %v, want [Slack]", quits)
	}
}

func TestDaemonEnforcePathAllowDropsDomainBlock(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "rules.json"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	mk := func(id string, action rules.Action, kind rules.TargetKind, value string) rules.Rule {
		return rules.Rule{
			ID: id, Action: action,
			Target:    rules.Target{Kind: kind, Value: value},
			Schedule:  rules.Schedule{Kind: rules.ScheduleAlways},
			CreatedAt: now, UpdatedAt: now,
		}
	}
	for _, r := range []rules.Rule{
		mk("r1", rules.ActionBlock, rules.TargetDomain, "reddit.com"),
		mk("r2", rules.ActionAllow, rules.TargetPath, "reddit.com/r/LocalLLaMA"),
		mk("r3", rules.ActionBlock, rules.TargetDomain, "twitter.com"),
	} {
		if err := st.Put(r); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}

	hm := hosts.New(newMemHostsFS(), "/etc/hosts")
	d := &Daemon{
		cfg:   Config{Logger: log.New(io.Discard, "", 0)},
		store: st,
		hosts: hm,
		mon:   &appmon.Monitor{Lister: &appmon.FakeLister{}, Quitter: &appmon.FakeQuitter{}},
		poke:  make(chan struct{}, 1),
	}
	d.enforce()

	doms, _ := hm.CurrentDomains()
	// reddit.com should NOT be in /etc/hosts because the allow narrows it.
	// twitter.com should still be blocked.
	if len(doms) != 1 || doms[0] != "twitter.com" {
		t.Errorf("expected only twitter.com blocked, got %v", doms)
	}
}

func TestDaemonEnforcePauseClearsDomains(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "rules.json"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	now := time.Now()
	rule := rules.Rule{
		ID:        "r1",
		Action:    rules.ActionBlock,
		Target:    rules.Target{Kind: rules.TargetDomain, Value: "twitter.com"},
		Schedule:  rules.Schedule{Kind: rules.ScheduleAlways},
		CreatedAt: now,
		UpdatedAt: now,
	}
	_ = st.Put(rule)

	hm := hosts.New(newMemHostsFS(), "/etc/hosts")
	mon := &appmon.Monitor{
		Lister:  &appmon.FakeLister{},
		Quitter: &appmon.FakeQuitter{},
	}
	d := &Daemon{
		cfg:   Config{Logger: log.New(io.Discard, "", 0)},
		store: st,
		hosts: hm,
		mon:   mon,
		poke:  make(chan struct{}, 1),
	}

	// Normal enforce: block lands on disk.
	d.enforce()
	doms, _ := hm.CurrentDomains()
	if len(doms) != 1 {
		t.Fatalf("pre-pause: expected 1 domain, got %v", doms)
	}

	// Pause: block should clear.
	pauseUntil := now.Add(time.Hour)
	_ = st.PutSettings(rules.Settings{PausedUntil: &pauseUntil})
	d.enforce()
	doms, _ = hm.CurrentDomains()
	if len(doms) != 0 {
		t.Errorf("during pause: expected no domains, got %v", doms)
	}
}

func TestDaemonRunStopsOnContextCancel(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{
		Addr:      pickAddr(t),
		StorePath: filepath.Join(dir, "rules.json"),
		HostsPath: filepath.Join(dir, "hosts"), // not /etc/hosts; just a tempfile
		TickEvery: 100 * time.Millisecond,
		Logger:    log.New(io.Discard, "", 0),
	}
	d, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://" + cfg.Addr + "/v1/status")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				goto served
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("daemon did not start serving")

served:
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("daemon did not stop after context cancel")
	}
}

func TestDaemonReenforceOnPoke(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{
		Addr:      pickAddr(t),
		StorePath: filepath.Join(dir, "rules.json"),
		HostsPath: filepath.Join(dir, "hosts"),
		TickEvery: time.Hour, // never tick — only the poke should trigger enforce
		Logger:    log.New(io.Discard, "", 0),
	}
	d, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()

	// Wait for daemon to be live.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://" + cfg.Addr + "/v1/status")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				goto served
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("daemon did not start serving")
served:
	// Add a rule via the store directly — the change hook should poke
	// the daemon and the hosts file should be written promptly.
	now := time.Now()
	_ = d.store.Put(rules.Rule{
		ID: "r1", Action: rules.ActionBlock,
		Target: rules.Target{Kind: rules.TargetDomain, Value: "x.com"},
		Schedule: rules.Schedule{Kind: rules.ScheduleAlways},
		CreatedAt: now, UpdatedAt: now,
	})

	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		doms, _ := d.hosts.CurrentDomains()
		if len(doms) == 1 && doms[0] == "x.com" {
			cancel()
			<-done
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("hosts file did not pick up the rule via poke")
}

func pickAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pickAddr: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}
