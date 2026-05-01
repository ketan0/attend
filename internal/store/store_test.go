package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ketan0/attend/internal/rules"
)

func tmpStore(t *testing.T) *FileStore {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "rules.json"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func sampleRule(id string) rules.Rule {
	return rules.Rule{
		ID:        id,
		Action:    rules.ActionBlock,
		Target:    rules.Target{Kind: rules.TargetDomain, Value: id + ".example"},
		Schedule:  rules.Schedule{Kind: rules.ScheduleAlways},
		CreatedAt: time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC),
	}
}

func TestStoreEmptyOpen(t *testing.T) {
	s := tmpStore(t)
	if got := s.List(); len(got) != 0 {
		t.Errorf("expected empty, got %d rules", len(got))
	}
}

func TestStorePutGet(t *testing.T) {
	s := tmpStore(t)
	r := sampleRule("r_1")
	if err := s.Put(r); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, ok := s.Get("r_1")
	if !ok {
		t.Fatalf("expected rule to exist")
	}
	if got.ID != r.ID {
		t.Errorf("ID = %q, want %q", got.ID, r.ID)
	}
}

func TestStorePersistAcrossOpens(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rules.json")

	s1, err := Open(path)
	if err != nil {
		t.Fatalf("Open 1: %v", err)
	}
	if err := s1.Put(sampleRule("r_1")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := s1.Put(sampleRule("r_2")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("Open 2: %v", err)
	}
	got := s2.List()
	if len(got) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(got))
	}
	if got[0].ID != "r_1" || got[1].ID != "r_2" {
		t.Errorf("order/ids wrong: %+v", got)
	}
}

func TestStoreDelete(t *testing.T) {
	s := tmpStore(t)
	_ = s.Put(sampleRule("r_1"))

	ok, err := s.Delete("r_1")
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if !ok {
		t.Errorf("expected ok=true")
	}
	if _, exists := s.Get("r_1"); exists {
		t.Errorf("expected gone")
	}

	ok, _ = s.Delete("nope")
	if ok {
		t.Errorf("expected ok=false for missing id")
	}
}

func TestStoreListOrder(t *testing.T) {
	s := tmpStore(t)
	t1 := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 5, 1, 13, 0, 0, 0, time.UTC)

	rA := sampleRule("r_A")
	rA.CreatedAt = t2
	rB := sampleRule("r_B")
	rB.CreatedAt = t1

	_ = s.Put(rA)
	_ = s.Put(rB)

	got := s.List()
	if got[0].ID != "r_B" || got[1].ID != "r_A" {
		t.Errorf("expected created_at asc: %+v", got)
	}
}

func TestStoreOnDiskFormat(t *testing.T) {
	s := tmpStore(t)
	_ = s.Put(sampleRule("r_1"))

	b, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var doc struct {
		Version int          `json:"version"`
		Rules   []rules.Rule `json:"rules"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if doc.Version != 1 {
		t.Errorf("version = %d, want 1", doc.Version)
	}
	if len(doc.Rules) != 1 {
		t.Errorf("rules len = %d, want 1", len(doc.Rules))
	}
}

func TestStoreConcurrentSafe(t *testing.T) {
	s := tmpStore(t)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := sampleRule("r_" + intStr(i))
			_ = s.Put(r)
			_, _ = s.Get(r.ID)
			_ = s.List()
		}(i)
	}
	wg.Wait()
	if got := len(s.List()); got != 50 {
		t.Errorf("expected 50 rules, got %d", got)
	}
}

func intStr(i int) string {
	if i == 0 {
		return "0"
	}
	digits := []byte{}
	for i > 0 {
		digits = append([]byte{byte('0' + i%10)}, digits...)
		i /= 10
	}
	return string(digits)
}
