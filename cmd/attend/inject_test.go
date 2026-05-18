package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ketan0/attend/internal/rules"
)

func TestCLIInjectAddInlineJS(t *testing.T) {
	stdout, stderr, err := runCLI(t,
		"inject", "add",
		"--match", "https://*.github.com/*",
		"--js", "console.log('hi')",
		"--name", "test",
	)
	if err != nil {
		t.Fatalf("err: %v stderr=%s", err, stderr)
	}
	var inj rules.Injection
	if err := json.Unmarshal([]byte(stdout), &inj); err != nil {
		t.Fatalf("decode: %v stdout=%q", err, stdout)
	}
	if !strings.HasPrefix(inj.ID, "inj_") {
		t.Errorf("ID = %q, want inj_*", inj.ID)
	}
	if inj.RunAt != rules.RunAtIdle {
		t.Errorf("run_at = %q", inj.RunAt)
	}
	if inj.World != rules.WorldMain {
		t.Errorf("world = %q", inj.World)
	}
	if inj.JS != "console.log('hi')" {
		t.Errorf("JS = %q", inj.JS)
	}
}

func TestCLIInjectAddFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "script.js")
	if err := writeFile(path, "alert(1)"); err != nil {
		t.Fatalf("write: %v", err)
	}
	stdout, _, err := runCLI(t,
		"inject", "add",
		"--match", "<all_urls>",
		"--js-file", path,
	)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var inj rules.Injection
	_ = json.Unmarshal([]byte(stdout), &inj)
	if inj.JS != "alert(1)" {
		t.Errorf("JS = %q", inj.JS)
	}
}

func TestCLIInjectAddRequiresMatch(t *testing.T) {
	_, _, err := runCLI(t, "inject", "add", "--js", "x")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCLIInjectAddRequiresPayload(t *testing.T) {
	_, _, err := runCLI(t, "inject", "add", "--match", "<all_urls>")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCLIInjectAddRejectsBadMatch(t *testing.T) {
	_, _, err := runCLI(t, "inject", "add", "--match", "garbage", "--js", "x")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCLIInjectLsAndRm(t *testing.T) {
	// Single in-process server reused across the calls would be ideal, but
	// runCLI spins up a fresh one per call. So just verify each command
	// works in isolation.
	stdout, _, err := runCLI(t, "inject", "ls")
	if err != nil {
		t.Fatalf("ls err: %v", err)
	}
	var list []rules.Injection
	if err := json.Unmarshal([]byte(stdout), &list); err != nil {
		t.Fatalf("decode: %v stdout=%q", err, stdout)
	}
	if len(list) != 0 {
		t.Errorf("expected empty, got %d", len(list))
	}

	_, _, err = runCLI(t, "inject", "rm", "inj_does_not_exist")
	if err == nil {
		t.Fatal("expected error for missing id")
	}
}

func writeFile(path, body string) error {
	return os.WriteFile(path, []byte(body), 0o600)
}
