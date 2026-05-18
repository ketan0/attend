package rules

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// MatchPattern is a Chrome match pattern, e.g. "https://*.github.com/*". We
// validate at the API boundary; the extension passes the string verbatim to
// chrome.scripting.registerContentScripts, which does the actual matching.
//
// Reference:
// https://developer.chrome.com/docs/extensions/develop/concepts/match-patterns
type MatchPattern string

var matchPatternRE = regexp.MustCompile(`^(\*|http|https|file|ftp|urn):\/\/([^\/]*)(\/.*)$`)

// Validate returns nil if the pattern is syntactically valid.
func (p MatchPattern) Validate() error {
	s := string(p)
	if s == "<all_urls>" {
		return nil
	}
	m := matchPatternRE.FindStringSubmatch(s)
	if m == nil {
		return fmt.Errorf("invalid match pattern %q: want <scheme>://<host>/<path> or <all_urls>", s)
	}
	scheme, host := m[1], m[2]
	if scheme == "file" {
		if host != "" {
			return fmt.Errorf("invalid match pattern %q: file:// scheme must have empty host", s)
		}
		return nil
	}
	if host == "" {
		return fmt.Errorf("invalid match pattern %q: host required", s)
	}
	if host == "*" {
		return nil
	}
	if strings.HasPrefix(host, "*.") {
		if strings.Contains(host[2:], "*") {
			return fmt.Errorf("invalid match pattern %q: '*' only allowed as host prefix '*.'", s)
		}
		return nil
	}
	if strings.Contains(host, "*") {
		return fmt.Errorf("invalid match pattern %q: '*' only allowed as host prefix '*.' or bare '*'", s)
	}
	return nil
}

// RunAt mirrors Chrome's content-script run_at field.
type RunAt string

const (
	RunAtStart RunAt = "document_start"
	RunAtEnd   RunAt = "document_end"
	RunAtIdle  RunAt = "document_idle"
)

func (r RunAt) Valid() bool {
	switch r {
	case RunAtStart, RunAtEnd, RunAtIdle:
		return true
	}
	return false
}

// World mirrors Chrome's execution-world field. MAIN runs in the page's JS
// realm (access to page globals like window.React); ISOLATED runs in a
// separate realm with DOM access but isolated globals — the default Chrome
// content-script behavior.
type World string

const (
	WorldMain     World = "MAIN"
	WorldIsolated World = "ISOLATED"
)

func (w World) Valid() bool {
	switch w {
	case WorldMain, WorldIsolated:
		return true
	}
	return false
}

// Injection is a persistent page modification. The daemon stores it; the
// extension hands it to chrome.scripting.registerContentScripts so Chrome
// handles dispatch, isolation, and run-at natively.
type Injection struct {
	ID        string         `json:"id"`
	Name      string         `json:"name,omitempty"`
	Match     []MatchPattern `json:"match"`
	Exclude   []MatchPattern `json:"exclude,omitempty"`
	RunAt     RunAt          `json:"run_at"`
	World     World          `json:"world"`
	AllFrames bool           `json:"all_frames"`
	JS        string         `json:"js,omitempty"`
	CSS       string         `json:"css,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

// Validate returns nil if the injection is well-formed.
func (i *Injection) Validate() error {
	if i.ID == "" {
		return fmt.Errorf("injection id is required")
	}
	if len(i.Match) == 0 {
		return fmt.Errorf("injection requires at least one match pattern")
	}
	for k, p := range i.Match {
		if err := p.Validate(); err != nil {
			return fmt.Errorf("match[%d]: %w", k, err)
		}
	}
	for k, p := range i.Exclude {
		if err := p.Validate(); err != nil {
			return fmt.Errorf("exclude[%d]: %w", k, err)
		}
	}
	if !i.RunAt.Valid() {
		return fmt.Errorf("invalid run_at %q (want document_start|document_end|document_idle)", i.RunAt)
	}
	if !i.World.Valid() {
		return fmt.Errorf("invalid world %q (want MAIN|ISOLATED)", i.World)
	}
	if strings.TrimSpace(i.JS) == "" && strings.TrimSpace(i.CSS) == "" {
		return fmt.Errorf("injection requires js or css")
	}
	return nil
}
