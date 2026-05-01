package hosts

import (
	"errors"
	"io/fs"
	"os"
	"strings"
	"sync"
	"testing"
)

// memFS is an in-memory FS for tests.
type memFS struct {
	mu    sync.Mutex
	files map[string][]byte
}

func newMem(initial map[string]string) *memFS {
	m := &memFS{files: map[string][]byte{}}
	for k, v := range initial {
		m.files[k] = []byte(v)
	}
	return m
}

func (m *memFS) ReadFile(path string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.files[path]
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: path, Err: os.ErrNotExist}
	}
	return append([]byte(nil), b...), nil
}

func (m *memFS) WriteFile(path string, data []byte, _ os.FileMode) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.files[path] = append([]byte(nil), data...)
	return nil
}

func TestApplyOnEmptyHosts(t *testing.T) {
	fs := newMem(nil)
	m := New(fs, "/etc/hosts")

	if err := m.Apply([]string{"twitter.com"}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got := string(fs.files["/etc/hosts"])
	if !strings.Contains(got, "0.0.0.0 twitter.com") {
		t.Errorf("missing IPv4 entry: %s", got)
	}
	if !strings.Contains(got, ":: twitter.com") {
		t.Errorf("missing IPv6 entry: %s", got)
	}
}

func TestApplyPreservesUserContent(t *testing.T) {
	original := `127.0.0.1 localhost
255.255.255.255 broadcasthost
::1 localhost
# user added
192.168.1.42 myrouter
`
	fs := newMem(map[string]string{"/etc/hosts": original})
	m := New(fs, "/etc/hosts")

	if err := m.Apply([]string{"twitter.com"}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	got := string(fs.files["/etc/hosts"])
	for _, line := range []string{
		"127.0.0.1 localhost",
		"255.255.255.255 broadcasthost",
		"# user added",
		"192.168.1.42 myrouter",
		"0.0.0.0 twitter.com",
	} {
		if !strings.Contains(got, line) {
			t.Errorf("missing %q in:\n%s", line, got)
		}
	}
}

func TestApplyReplacesExistingBlock(t *testing.T) {
	original := `127.0.0.1 localhost
` + beginMarker + `
0.0.0.0 oldsite.com
:: oldsite.com
` + endMarker + `
192.168.1.1 router
`
	fs := newMem(map[string]string{"/etc/hosts": original})
	m := New(fs, "/etc/hosts")

	if err := m.Apply([]string{"newsite.com"}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got := string(fs.files["/etc/hosts"])
	if strings.Contains(got, "oldsite.com") {
		t.Errorf("old domain still present:\n%s", got)
	}
	if !strings.Contains(got, "0.0.0.0 newsite.com") {
		t.Errorf("new domain not added:\n%s", got)
	}
	if !strings.Contains(got, "192.168.1.1 router") {
		t.Errorf("user content after block lost:\n%s", got)
	}
	if !strings.Contains(got, "127.0.0.1 localhost") {
		t.Errorf("user content before block lost:\n%s", got)
	}
}

func TestApplyEmptyRemovesBlock(t *testing.T) {
	original := `127.0.0.1 localhost
` + beginMarker + `
0.0.0.0 oldsite.com
:: oldsite.com
` + endMarker + `
192.168.1.1 router
`
	fs := newMem(map[string]string{"/etc/hosts": original})
	m := New(fs, "/etc/hosts")

	if err := m.Apply(nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got := string(fs.files["/etc/hosts"])
	if strings.Contains(got, beginMarker) || strings.Contains(got, endMarker) {
		t.Errorf("markers should be removed:\n%s", got)
	}
	if strings.Contains(got, "oldsite.com") {
		t.Errorf("domains should be removed:\n%s", got)
	}
	if !strings.Contains(got, "127.0.0.1 localhost") || !strings.Contains(got, "192.168.1.1 router") {
		t.Errorf("user content should remain:\n%s", got)
	}
}

func TestApplyDedupAndNormalize(t *testing.T) {
	fs := newMem(nil)
	m := New(fs, "/etc/hosts")

	if err := m.Apply([]string{
		"Twitter.com",
		"twitter.com",
		" https://twitter.com ",
		"reddit.com/r/all", // path stripped
		"",                 // dropped
	}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	got := string(fs.files["/etc/hosts"])
	if strings.Count(got, "0.0.0.0 twitter.com") != 1 {
		t.Errorf("twitter should appear once, got:\n%s", got)
	}
	if !strings.Contains(got, "0.0.0.0 reddit.com") {
		t.Errorf("path entry should reduce to domain:\n%s", got)
	}
}

func TestCurrentDomainsRoundTrip(t *testing.T) {
	fs := newMem(nil)
	m := New(fs, "/etc/hosts")

	in := []string{"twitter.com", "x.com", "reddit.com"}
	if err := m.Apply(in); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got, err := m.CurrentDomains()
	if err != nil {
		t.Fatalf("CurrentDomains: %v", err)
	}
	want := []string{"reddit.com", "twitter.com", "x.com"} // sorted
	if len(got) != len(want) {
		t.Fatalf("len mismatch: got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] %q != %q", i, got[i], want[i])
		}
	}
}

func TestApplyIdempotent(t *testing.T) {
	fs := newMem(map[string]string{"/etc/hosts": "127.0.0.1 localhost\n"})
	m := New(fs, "/etc/hosts")

	if err := m.Apply([]string{"x.com", "y.com"}); err != nil {
		t.Fatalf("Apply 1: %v", err)
	}
	first := string(fs.files["/etc/hosts"])
	if err := m.Apply([]string{"x.com", "y.com"}); err != nil {
		t.Fatalf("Apply 2: %v", err)
	}
	second := string(fs.files["/etc/hosts"])
	if first != second {
		t.Errorf("expected idempotent rewrites:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

func TestCurrentDomainsNoFile(t *testing.T) {
	fs := newMem(nil)
	m := New(fs, "/etc/hosts")
	got, err := m.CurrentDomains()
	if err != nil {
		t.Fatalf("CurrentDomains: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
}

// Sanity: confirm OSFS calls work against tempdir (non-/etc/hosts).
func TestOSFSWorksAgainstTempfile(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/fake-hosts"
	if err := os.WriteFile(path, []byte("127.0.0.1 localhost\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	m := New(OSFS{}, path)
	if err := m.Apply([]string{"foo.com"}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(b), "0.0.0.0 foo.com") {
		t.Errorf("missing entry: %s", b)
	}
	got, err := m.CurrentDomains()
	if err != nil {
		t.Fatalf("CurrentDomains: %v", err)
	}
	if len(got) != 1 || got[0] != "foo.com" {
		t.Errorf("CurrentDomains = %v, want [foo.com]", got)
	}
}

// Verify error path: reading a path with a permissions error propagates.
type erroringFS struct{}

func (erroringFS) ReadFile(string) ([]byte, error) { return nil, errors.New("boom") }
func (erroringFS) WriteFile(string, []byte, os.FileMode) error {
	return errors.New("boom")
}

func TestApplyPropagatesReadError(t *testing.T) {
	m := New(erroringFS{}, "/etc/hosts")
	if err := m.Apply([]string{"x.com"}); err == nil {
		t.Errorf("expected error, got nil")
	}
}
