package main

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ketan0/attend/internal/rules"
	"github.com/ketan0/attend/internal/server"
	"github.com/ketan0/attend/internal/store"
)

// runCLI runs `attend <args...>` against a fresh in-process server and
// returns (stdout, stderr, error).
func runCLI(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "rules.json"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	srv := server.New(st)
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)

	root := newRoot()
	root.SetArgs(append([]string{"--url", hs.URL}, args...))
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	root.SetOut(stdout)
	root.SetErr(stderr)
	err = root.Execute()
	return stdout.String(), stderr.String(), err
}

func TestCLIBlockEmitsRuleJSON(t *testing.T) {
	stdout, stderr, err := runCLI(t, "block", "twitter.com", "--for", "2h")
	if err != nil {
		t.Fatalf("err: %v stderr=%s", err, stderr)
	}
	var r rules.Rule
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		t.Fatalf("decode: %v stdout=%q", err, stdout)
	}
	if r.Action != rules.ActionBlock {
		t.Errorf("action = %q", r.Action)
	}
	if r.Target.Kind != rules.TargetDomain || r.Target.Value != "twitter.com" {
		t.Errorf("target = %+v", r.Target)
	}
	if r.Schedule.Kind != rules.ScheduleUntil {
		t.Errorf("schedule kind = %q", r.Schedule.Kind)
	}
}

func TestCLIPathTargetAutoDetect(t *testing.T) {
	stdout, _, err := runCLI(t, "block", "reddit.com/r/all")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var r rules.Rule
	_ = json.Unmarshal([]byte(stdout), &r)
	if r.Target.Kind != rules.TargetPath {
		t.Errorf("expected path kind, got %q", r.Target.Kind)
	}
}

func TestCLIAppPrefix(t *testing.T) {
	stdout, _, err := runCLI(t, "block", "app:Slack")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var r rules.Rule
	_ = json.Unmarshal([]byte(stdout), &r)
	if r.Target.Kind != rules.TargetApp || r.Target.Value != "Slack" {
		t.Errorf("target = %+v", r.Target)
	}
}

func TestCLIFrictionDefaults(t *testing.T) {
	stdout, _, err := runCLI(t, "friction", "reddit.com")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var r rules.Rule
	_ = json.Unmarshal([]byte(stdout), &r)
	if r.Friction == nil || r.Friction.Level != rules.FrictionIntent {
		t.Errorf("expected default level=intent, got %+v", r.Friction)
	}
	// Cooldown should default to 5m.
	if r.Friction.Cooldown.Std().Minutes() != 5 {
		t.Errorf("expected 5m cooldown, got %v", r.Friction.Cooldown)
	}
}

func TestCLINudgeRequiresMessage(t *testing.T) {
	_, _, err := runCLI(t, "nudge", "youtube.com")
	if err == nil {
		t.Errorf("expected error when --message missing")
	}
}

func TestCLIConflictBlockedWithoutReplace(t *testing.T) {
	dir := t.TempDir()
	st, _ := store.Open(filepath.Join(dir, "rules.json"))
	srv := server.New(st)
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)

	run := func(args ...string) (string, string, error) {
		root := newRoot()
		root.SetArgs(append([]string{"--url", hs.URL}, args...))
		stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
		root.SetOut(stdout)
		root.SetErr(stderr)
		return stdout.String(), stderr.String(), root.Execute()
	}

	if _, _, err := run("block", "twitter.com"); err != nil {
		t.Fatalf("first block: %v", err)
	}
	_, stderr, err := run("block", "twitter.com")
	if err == nil {
		t.Fatalf("expected conflict error")
	}
	if !strings.Contains(stderr+err.Error(), "replace") {
		t.Errorf("error should mention replace, got stderr=%q err=%v", stderr, err)
	}
	if _, _, err := run("block", "twitter.com", "--replace"); err != nil {
		t.Errorf("replace should succeed: %v", err)
	}
}

func TestCLIStatusPauseResume(t *testing.T) {
	dir := t.TempDir()
	st, _ := store.Open(filepath.Join(dir, "rules.json"))
	srv := server.New(st)
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)

	run := func(args ...string) string {
		t.Helper()
		root := newRoot()
		root.SetArgs(append([]string{"--url", hs.URL}, args...))
		stdout := &bytes.Buffer{}
		root.SetOut(stdout)
		root.SetErr(&bytes.Buffer{})
		if err := root.Execute(); err != nil {
			t.Fatalf("err: %v", err)
		}
		return stdout.String()
	}

	run("pause", "--for", "30m")
	out := run("status")
	if !strings.Contains(out, `"paused": true`) {
		t.Errorf("expected paused=true in status, got %s", out)
	}
	run("resume")
	out = run("status")
	if !strings.Contains(out, `"paused": false`) {
		t.Errorf("expected paused=false in status, got %s", out)
	}
}

func TestCLIAllowCarveOutCoexistsWithBlock(t *testing.T) {
	dir := t.TempDir()
	st, _ := store.Open(filepath.Join(dir, "rules.json"))
	srv := server.New(st)
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)

	run := func(args ...string) (string, error) {
		root := newRoot()
		root.SetArgs(append([]string{"--url", hs.URL}, args...))
		stdout := &bytes.Buffer{}
		root.SetOut(stdout)
		root.SetErr(&bytes.Buffer{})
		err := root.Execute()
		return stdout.String(), err
	}

	if _, err := run("block", "reddit.com"); err != nil {
		t.Fatalf("block: %v", err)
	}
	// Allow on a *different* canonical target (path) should NOT conflict
	// with the domain block — they have different canonicals.
	out, err := run("allow", "reddit.com/r/LocalLLaMA")
	if err != nil {
		t.Fatalf("allow: %v out=%s", err, out)
	}
	all, _ := run("ls")
	var rs []rules.Rule
	_ = json.Unmarshal([]byte(all), &rs)
	if len(rs) != 2 {
		t.Errorf("expected 2 rules, got %d", len(rs))
	}
	hasBlock, hasAllow := false, false
	for _, r := range rs {
		if r.Action == rules.ActionBlock && r.Target.Kind == rules.TargetDomain {
			hasBlock = true
		}
		if r.Action == rules.ActionAllow && r.Target.Kind == rules.TargetPath {
			hasAllow = true
		}
	}
	if !hasBlock || !hasAllow {
		t.Errorf("expected one block + one allow, got %+v", rs)
	}
}

func TestCLIAllowSameTargetAsBlockConflicts(t *testing.T) {
	dir := t.TempDir()
	st, _ := store.Open(filepath.Join(dir, "rules.json"))
	srv := server.New(st)
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)

	run := func(args ...string) error {
		root := newRoot()
		root.SetArgs(append([]string{"--url", hs.URL}, args...))
		root.SetOut(&bytes.Buffer{})
		root.SetErr(&bytes.Buffer{})
		return root.Execute()
	}
	if err := run("block", "twitter.com"); err != nil {
		t.Fatalf("block: %v", err)
	}
	// Same canonical target — should conflict, even with different action.
	if err := run("allow", "twitter.com"); err == nil {
		t.Errorf("expected conflict when block + allow target same canonical")
	}
	// --replace swaps the existing block for an allow.
	if err := run("allow", "twitter.com", "--replace"); err != nil {
		t.Errorf("allow --replace: %v", err)
	}
}

func TestParseTargetForms(t *testing.T) {
	cases := []struct {
		in   string
		kind rules.TargetKind
		val  string
		err  bool
	}{
		{"twitter.com", rules.TargetDomain, "twitter.com", false},
		{"reddit.com/r/all", rules.TargetPath, "reddit.com/r/all", false},
		{"app:Slack", rules.TargetApp, "Slack", false},
		{"domain:foo.com", rules.TargetDomain, "foo.com", false},
		{"path:foo.com/bar", rules.TargetPath, "foo.com/bar", false},
		{"", rules.TargetKind(""), "", true},
		{"app:", rules.TargetKind(""), "", true},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			tg, err := parseTarget(c.in)
			if c.err {
				if err == nil {
					t.Errorf("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if tg.Kind != c.kind || tg.Value != c.val {
				t.Errorf("got %+v", tg)
			}
		})
	}
}
