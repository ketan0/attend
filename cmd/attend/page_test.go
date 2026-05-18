package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ketan0/attend/internal/client"
	"github.com/ketan0/attend/internal/jobs"
	"github.com/ketan0/attend/internal/server"
	"github.com/ketan0/attend/internal/store"
)

// runCLIWithExtension spins up a daemon, starts a fake "extension" goroutine
// that consumes jobs from the queue and responds via the handler provided,
// and runs `attend <args>` against the daemon.
func runCLIWithExtension(
	t *testing.T,
	handler func(j jobs.Job) jobs.Result,
	args ...string,
) (string, string, error) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "rules.json"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	srv := server.New(st)
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)

	// Fake extension: pull jobs, run handler, post result.
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		for {
			job, ok := srv.PageJobs.Next(ctx)
			if !ok {
				return
			}
			srv.PageJobs.PostResult(job.ID, handler(job))
		}
	}()

	root := newRoot()
	root.SetArgs(append([]string{"--url", hs.URL}, args...))
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	root.SetOut(stdout)
	root.SetErr(stderr)
	err = root.Execute()
	return stdout.String(), stderr.String(), err
}

func TestCLIPageTabs(t *testing.T) {
	handler := func(j jobs.Job) jobs.Result {
		if j.Kind != "tabs.list" {
			return jobs.Result{Ok: false, Error: "wrong kind: " + j.Kind}
		}
		return jobs.Result{Ok: true, Value: json.RawMessage(
			`[{"tab_id":42,"url":"https://example.com/","title":"Example","active":true,"window_id":1}]`,
		)}
	}
	stdout, stderr, err := runCLIWithExtension(t, handler, "page", "tabs")
	if err != nil {
		t.Fatalf("err: %v stderr=%s", err, stderr)
	}
	var got []map[string]any
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("decode: %v stdout=%q", err, stdout)
	}
	if len(got) != 1 || got[0]["tab_id"].(float64) != 42 {
		t.Errorf("got %+v", got)
	}
}

func TestCLIPageDumpWritesTempFile(t *testing.T) {
	html := "<html><body>hi</body></html>"
	handler := func(j jobs.Job) jobs.Result {
		if j.Kind != "page.dump" {
			return jobs.Result{Ok: false, Error: "wrong kind: " + j.Kind}
		}
		v, _ := json.Marshal(map[string]any{
			"tab_id": 7,
			"url":    "https://example.com/",
			"title":  "Example",
			"html":   html,
		})
		return jobs.Result{Ok: true, Value: v}
	}
	stdout, _, err := runCLIWithExtension(t, handler, "page", "dump")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var out struct {
		TabID int    `json:"tab_id"`
		URL   string `json:"url"`
		File  string `json:"file"`
		Bytes int    `json:"bytes"`
	}
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("decode: %v stdout=%q", err, stdout)
	}
	if out.TabID != 7 || out.Bytes != len(html) {
		t.Errorf("got %+v", out)
	}
	if !strings.HasPrefix(out.File, "/") || !strings.Contains(out.File, "attend-page-7-") {
		t.Errorf("file path looks wrong: %q", out.File)
	}
}

func TestCLIPageExecReturnsValue(t *testing.T) {
	handler := func(j jobs.Job) jobs.Result {
		if j.Kind != "page.exec" {
			return jobs.Result{Ok: false, Error: "wrong kind: " + j.Kind}
		}
		v, _ := json.Marshal(map[string]any{
			"tab_id": 9,
			"url":    "https://example.com/",
			"value":  json.RawMessage(`"hello"`),
		})
		return jobs.Result{Ok: true, Value: v}
	}
	stdout, _, err := runCLIWithExtension(t, handler, "page", "exec", "--js", "document.title")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var out struct {
		TabID int    `json:"tab_id"`
		Value string `json:"value"`
	}
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("decode: %v stdout=%q", err, stdout)
	}
	if out.TabID != 9 || out.Value != "hello" {
		t.Errorf("got %+v", out)
	}
}

func TestCLIPageExecRequiresPayload(t *testing.T) {
	called := false
	handler := func(j jobs.Job) jobs.Result {
		called = true
		return jobs.Result{Ok: true, Value: json.RawMessage(`{}`)}
	}
	_, _, err := runCLIWithExtension(t, handler, "page", "exec")
	if err == nil {
		t.Fatal("expected error")
	}
	if called {
		t.Error("handler should not have been called")
	}
}

func TestCLIPageRespectsExplicitOutFile(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "page.html")
	handler := func(j jobs.Job) jobs.Result {
		v, _ := json.Marshal(map[string]any{
			"tab_id": 1,
			"url":    "https://example.com/",
			"title":  "",
			"html":   "<x/>",
		})
		return jobs.Result{Ok: true, Value: v}
	}
	stdout, _, err := runCLIWithExtension(t, handler, "page", "dump", "--out", dest)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var out struct {
		File string `json:"file"`
	}
	_ = json.Unmarshal([]byte(stdout), &out)
	if out.File != dest {
		t.Errorf("file = %q, want %q", out.File, dest)
	}
}

// silence unused-import warnings in case future tests drop a dep.
var _ = client.New
var _ = time.Second
