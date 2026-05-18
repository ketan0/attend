package rules

import (
	"strings"
	"testing"
	"time"
)

func TestMatchPatternValidate(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		wantErr bool
	}{
		{"all_urls", "<all_urls>", false},
		{"https exact", "https://github.com/*", false},
		{"https wildcard subdomain", "https://*.github.com/*", false},
		{"any scheme", "*://example.com/*", false},
		{"http only path", "http://example.com/foo", false},
		{"bare host wildcard", "https://*/*", false},
		{"file empty host", "file:///*", false},
		{"file with path", "file:///Users/me/*", false},

		{"empty", "", true},
		{"no scheme", "github.com/*", true},
		{"unknown scheme", "ws://example.com/*", true},
		{"missing path", "https://example.com", true},
		{"file with host", "file://example.com/foo", true},
		{"wildcard mid host", "https://foo.*.com/*", true},
		{"wildcard suffix host", "https://github.*/*", true},
		{"double wildcard host", "https://*.*.github.com/*", true},
		{"missing host", "https:///foo", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := MatchPattern(tc.pattern).Validate()
			if tc.wantErr && err == nil {
				t.Errorf("expected error for %q, got nil", tc.pattern)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error for %q: %v", tc.pattern, err)
			}
		})
	}
}

func TestRunAtValid(t *testing.T) {
	good := []RunAt{RunAtStart, RunAtEnd, RunAtIdle}
	for _, r := range good {
		if !r.Valid() {
			t.Errorf("%q should be valid", r)
		}
	}
	for _, r := range []RunAt{"", "document_load", "idle"} {
		if r.Valid() {
			t.Errorf("%q should not be valid", r)
		}
	}
}

func TestWorldValid(t *testing.T) {
	for _, w := range []World{WorldMain, WorldIsolated} {
		if !w.Valid() {
			t.Errorf("%q should be valid", w)
		}
	}
	for _, w := range []World{"", "main", "isolated", "page"} {
		if w.Valid() {
			t.Errorf("%q should not be valid", w)
		}
	}
}

func sampleInjection() Injection {
	return Injection{
		ID:        "inj_1",
		Name:      "test",
		Match:     []MatchPattern{"https://*.github.com/*"},
		RunAt:     RunAtIdle,
		World:     WorldMain,
		JS:        "console.log('hi')",
		CreatedAt: time.Date(2026, 5, 17, 0, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 5, 17, 0, 0, 0, 0, time.UTC),
	}
}

func TestInjectionValidate(t *testing.T) {
	cases := []struct {
		name   string
		mut    func(*Injection)
		errSub string // substring required in error; "" = expect no error
	}{
		{"baseline", func(*Injection) {}, ""},
		{"css only", func(i *Injection) { i.JS = ""; i.CSS = "body { background: red }" }, ""},
		{"both", func(i *Injection) { i.CSS = "body { color: blue }" }, ""},
		{"with exclude", func(i *Injection) {
			i.Exclude = []MatchPattern{"https://api.github.com/*"}
		}, ""},

		{"no id", func(i *Injection) { i.ID = "" }, "id is required"},
		{"no match", func(i *Injection) { i.Match = nil }, "at least one match"},
		{"bad match", func(i *Injection) {
			i.Match = []MatchPattern{"not a pattern"}
		}, "match[0]"},
		{"bad exclude", func(i *Injection) {
			i.Exclude = []MatchPattern{"also bad"}
		}, "exclude[0]"},
		{"bad run_at", func(i *Injection) { i.RunAt = "soon" }, "run_at"},
		{"bad world", func(i *Injection) { i.World = "page" }, "world"},
		{"no payload", func(i *Injection) { i.JS = ""; i.CSS = "" }, "js or css"},
		{"whitespace payload", func(i *Injection) { i.JS = "   \n\t  " }, "js or css"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inj := sampleInjection()
			tc.mut(&inj)
			err := inj.Validate()
			if tc.errSub == "" {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.errSub)
			}
			if !strings.Contains(err.Error(), tc.errSub) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.errSub)
			}
		})
	}
}
