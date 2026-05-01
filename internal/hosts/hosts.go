// Package hosts manages an attend-owned section of /etc/hosts.
//
// Design: every line we add is sandwiched between a fixed BEGIN/END marker so
// we can atomically rewrite our section without touching the rest of the file.
// All file I/O is done via the FS interface so tests can use an in-memory
// implementation; the real daemon uses OSFS.
package hosts

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

const (
	beginMarker = "# BEGIN attend-managed (do not edit) "
	endMarker   = "# END attend-managed"
)

// FS abstracts the host filesystem so we can test against an in-memory fake.
type FS interface {
	ReadFile(path string) ([]byte, error)
	WriteFile(path string, data []byte, perm os.FileMode) error
}

// OSFS implements FS via the real filesystem.
type OSFS struct{}

func (OSFS) ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }
func (OSFS) WriteFile(path string, data []byte, perm os.FileMode) error {
	return os.WriteFile(path, data, perm)
}

// Manager is a hosts-file editor.
type Manager struct {
	FS   FS
	Path string // typically "/etc/hosts"
}

// New builds a Manager with the given filesystem and path.
func New(fs FS, path string) *Manager { return &Manager{FS: fs, Path: path} }

// Apply replaces the attend-managed block with one entry per domain pointing
// to 0.0.0.0 (and ::). Domains are deduplicated and sorted. An empty list
// removes the block entirely.
func (m *Manager) Apply(domains []string) error {
	clean := normalizeDomains(domains)

	raw, err := m.FS.ReadFile(m.Path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", m.Path, err)
	}

	prefix, suffix := splitOutManaged(raw)
	var buf bytes.Buffer
	buf.Write(prefix)
	if len(clean) > 0 {
		buf.WriteString(beginMarker)
		buf.WriteString("\n")
		for _, d := range clean {
			fmt.Fprintf(&buf, "0.0.0.0 %s\n", d)
			fmt.Fprintf(&buf, ":: %s\n", d)
		}
		buf.WriteString(endMarker)
		buf.WriteString("\n")
	}
	if len(suffix) > 0 {
		// Ensure exactly one newline between our block and following content.
		if buf.Len() > 0 && !bytes.HasSuffix(buf.Bytes(), []byte("\n")) {
			buf.WriteByte('\n')
		}
		buf.Write(suffix)
	}

	return m.FS.WriteFile(m.Path, buf.Bytes(), 0o644)
}

// CurrentDomains returns the domains currently inside the attend-managed
// block, in sorted order.
func (m *Manager) CurrentDomains() ([]string, error) {
	raw, err := m.FS.ReadFile(m.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	_, _ = io.Discard, bufio.ScanLines // keep imports tidy if we extend
	managed := extractManaged(raw)

	seen := map[string]struct{}{}
	for _, line := range strings.Split(string(managed), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if fields[0] == "0.0.0.0" || fields[0] == "::" {
			seen[fields[1]] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for d := range seen {
		out = append(out, d)
	}
	sort.Strings(out)
	return out, nil
}

// normalizeDomains lowercases, trims, and dedups; preserves sort order.
func normalizeDomains(in []string) []string {
	seen := map[string]struct{}{}
	for _, d := range in {
		d = strings.TrimSpace(strings.ToLower(d))
		d = strings.TrimPrefix(d, "http://")
		d = strings.TrimPrefix(d, "https://")
		// Strip any path component if a TargetPath leaked through.
		if i := strings.IndexByte(d, '/'); i >= 0 {
			d = d[:i]
		}
		if d == "" {
			continue
		}
		seen[d] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for d := range seen {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

// splitOutManaged returns (everything before the managed block, everything
// after the managed block). If no managed block exists, suffix is empty and
// prefix is the entire file.
func splitOutManaged(raw []byte) (prefix, suffix []byte) {
	beginIdx := bytes.Index(raw, []byte(beginMarker))
	if beginIdx < 0 {
		return raw, nil
	}
	// Trim trailing newline before the block from the prefix to avoid
	// double-newlines when we re-emit.
	prefix = raw[:beginIdx]
	for len(prefix) > 0 && prefix[len(prefix)-1] == '\n' {
		prefix = prefix[:len(prefix)-1]
	}
	if len(prefix) > 0 {
		prefix = append(prefix, '\n')
	}

	endIdx := bytes.Index(raw[beginIdx:], []byte(endMarker))
	if endIdx < 0 {
		// Malformed: BEGIN with no END. Treat rest of file as managed.
		return prefix, nil
	}
	endIdx += beginIdx + len(endMarker)
	if endIdx < len(raw) && raw[endIdx] == '\n' {
		endIdx++
	}
	suffix = raw[endIdx:]
	return prefix, suffix
}

// extractManaged returns the bytes inside the managed block (between markers).
func extractManaged(raw []byte) []byte {
	beginIdx := bytes.Index(raw, []byte(beginMarker))
	if beginIdx < 0 {
		return nil
	}
	beginIdx += len(beginMarker)
	endIdx := bytes.Index(raw[beginIdx:], []byte(endMarker))
	if endIdx < 0 {
		return raw[beginIdx:]
	}
	return raw[beginIdx : beginIdx+endIdx]
}
