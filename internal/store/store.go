// Package store persists attend rules to disk. It is safe for concurrent use
// by multiple goroutines within a single process; cross-process safety is
// not a concern because only attendd writes here.
package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/ketan0/attend/internal/rules"
)

// Store is the rule persistence interface, kept narrow so the HTTP layer can
// be tested against an in-memory fake.
type Store interface {
	List() []rules.Rule
	Get(id string) (rules.Rule, bool)
	Put(r rules.Rule) error
	Delete(id string) (bool, error)
	Settings() rules.Settings
	PutSettings(s rules.Settings) error
}

// FileStore stores rules as a single JSON document on disk. All public
// methods are safe to call concurrently.
type FileStore struct {
	path     string
	mu       sync.RWMutex
	rs       map[string]rules.Rule
	settings rules.Settings
	// onChange, if set, is called (non-blocking) after every successful
	// write so the daemon can re-enforce promptly.
	onChange func()
}

// SetChangeHook installs a hook called after every successful write. It is
// invoked in a goroutine so a slow hook does not block writes.
func (s *FileStore) SetChangeHook(fn func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onChange = fn
}

// Open loads the store at `path`, creating an empty file if it does not exist.
// The parent directory is created if missing.
func Open(path string) (*FileStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir parent: %w", err)
	}
	s := &FileStore{path: path, rs: map[string]rules.Rule{}}

	b, err := os.ReadFile(path)
	if errIsNotExist(err) {
		return s, s.flushLocked()
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if len(b) == 0 {
		return s, nil
	}

	var doc fileDoc
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	for _, r := range doc.Rules {
		s.rs[r.ID] = r
	}
	s.settings = doc.Settings
	return s, nil
}

// Path returns the on-disk path the store is reading/writing.
func (s *FileStore) Path() string { return s.path }

// List returns rules in stable order (by created_at ascending, then ID).
func (s *FileStore) List() []rules.Rule {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]rules.Rule, 0, len(s.rs))
	for _, r := range s.rs {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Get returns the rule with `id`, or false if not present.
func (s *FileStore) Get(id string) (rules.Rule, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.rs[id]
	return r, ok
}

// Put inserts or replaces the rule with id == r.ID and persists to disk.
func (s *FileStore) Put(r rules.Rule) error {
	if r.ID == "" {
		return fmt.Errorf("rule id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rs[r.ID] = r
	return s.flushLocked()
}

// Delete removes the rule and persists. Returns false if it did not exist.
func (s *FileStore) Delete(id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.rs[id]; !ok {
		return false, nil
	}
	delete(s.rs, id)
	return true, s.flushLocked()
}

// Settings returns a snapshot of daemon settings.
func (s *FileStore) Settings() rules.Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.settings
}

// PutSettings replaces the settings and persists.
func (s *FileStore) PutSettings(set rules.Settings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settings = set
	return s.flushLocked()
}

// fileDoc is the on-disk shape.
type fileDoc struct {
	Version  int            `json:"version"`
	Settings rules.Settings `json:"settings"`
	Rules    []rules.Rule   `json:"rules"`
}

// notifyChangeLocked schedules onChange asynchronously. Caller must hold the
// write lock; the hook itself runs without it.
func (s *FileStore) notifyChangeLocked() {
	if s.onChange == nil {
		return
	}
	hook := s.onChange
	go hook()
}

// flushLocked writes the current state to disk atomically. Caller must hold
// the write lock.
func (s *FileStore) flushLocked() error {
	doc := fileDoc{Version: 1, Settings: s.settings, Rules: make([]rules.Rule, 0, len(s.rs))}
	for _, r := range s.rs {
		doc.Rules = append(doc.Rules, r)
	}
	sort.Slice(doc.Rules, func(i, j int) bool { return doc.Rules[i].ID < doc.Rules[j].ID })

	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".rules-*.json.tmp")
	if err != nil {
		return fmt.Errorf("tempfile: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }

	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("write tempfile: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("fsync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close tempfile: %w", err)
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		cleanup()
		return fmt.Errorf("rename: %w", err)
	}
	s.notifyChangeLocked()
	return nil
}

func errIsNotExist(err error) bool {
	return err != nil && os.IsNotExist(err)
}
