package client

import (
	"context"
	"path/filepath"
	"testing"

	"net/http/httptest"

	"github.com/ketan0/attend/internal/rules"
	"github.com/ketan0/attend/internal/server"
	"github.com/ketan0/attend/internal/store"
)

func startBackend(t *testing.T) (*Client, func()) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "rules.json"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	srv := server.New(st)
	hs := httptest.NewServer(srv.Handler())
	c := New(hs.URL)
	return c, hs.Close
}

func TestCreateAndGet(t *testing.T) {
	c, stop := startBackend(t)
	defer stop()

	created, err := c.CreateRule(context.Background(), server.CreateRuleRequest{
		Action: rules.ActionBlock,
		Target: rules.Target{Kind: rules.TargetDomain, Value: "twitter.com"},
	})
	if err != nil {
		t.Fatalf("CreateRule: %v", err)
	}
	if created.ID == "" {
		t.Fatal("missing ID")
	}

	got, err := c.GetRule(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("GetRule: %v", err)
	}
	if got.ID != created.ID {
		t.Errorf("id mismatch: %q vs %q", got.ID, created.ID)
	}
}

func TestListAndDelete(t *testing.T) {
	c, stop := startBackend(t)
	defer stop()

	for _, d := range []string{"a.com", "b.com", "c.com"} {
		_, err := c.CreateRule(context.Background(), server.CreateRuleRequest{
			Action: rules.ActionBlock,
			Target: rules.Target{Kind: rules.TargetDomain, Value: d},
		})
		if err != nil {
			t.Fatalf("CreateRule: %v", err)
		}
	}

	all, err := c.ListRules(context.Background())
	if err != nil {
		t.Fatalf("ListRules: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("expected 3, got %d", len(all))
	}

	if err := c.DeleteRule(context.Background(), all[0].ID); err != nil {
		t.Fatalf("DeleteRule: %v", err)
	}
	all, _ = c.ListRules(context.Background())
	if len(all) != 2 {
		t.Errorf("after delete expected 2, got %d", len(all))
	}

	if err := c.DeleteRule(context.Background(), "nope"); !IsAPIError(err, "not_found") {
		t.Errorf("expected not_found APIError, got %v", err)
	}
}

func TestConflictDetection(t *testing.T) {
	c, stop := startBackend(t)
	defer stop()

	_, err := c.CreateRule(context.Background(), server.CreateRuleRequest{
		Action: rules.ActionBlock,
		Target: rules.Target{Kind: rules.TargetDomain, Value: "x.com"},
	})
	if err != nil {
		t.Fatalf("CreateRule: %v", err)
	}
	_, err = c.CreateRule(context.Background(), server.CreateRuleRequest{
		Action: rules.ActionBlock,
		Target: rules.Target{Kind: rules.TargetDomain, Value: "x.com"},
	})
	if !IsConflict(err) {
		t.Errorf("expected conflict, got %v", err)
	}
}

func TestPauseStatus(t *testing.T) {
	c, stop := startBackend(t)
	defer stop()

	_, err := c.Pause(context.Background(), server.PauseRequest{For: "30m"})
	if err != nil {
		t.Fatalf("Pause: %v", err)
	}
	s, err := c.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !s.Paused {
		t.Errorf("expected paused")
	}
	if _, err := c.Resume(context.Background()); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	s, _ = c.Status(context.Background())
	if s.Paused {
		t.Errorf("expected resumed")
	}
}

// IsAPIError is a small helper for callers (and these tests) that don't want to
// import errors and reflect on the type. Promote later if more code wants it.
func IsAPIError(err error, code string) bool {
	if err == nil {
		return false
	}
	type coded interface {
		Error() string
	}
	var ae *APIError
	if e, ok := err.(*APIError); ok {
		ae = e
	} else {
		_ = coded(err)
	}
	return ae != nil && ae.Code == code
}
