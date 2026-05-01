// Package appmon polls running macOS apps and quits any whose name appears
// in the blocked list. The polling and quit primitives are abstracted behind
// interfaces so the decision logic is testable on any platform.
package appmon

import (
	"fmt"
	"os/exec"
	"strings"
	"sync"
)

// Lister returns the names of currently-running user-visible apps.
type Lister interface {
	RunningApps() ([]string, error)
}

// Quitter requests an app exit by name.
type Quitter interface {
	Quit(name string) error
}

// SweepResult is returned for each blocked app encountered during a sweep.
type SweepResult struct {
	App      string
	Quit     bool
	QuitErr  error
}

// Monitor combines a Lister and Quitter and exposes a single Sweep call.
type Monitor struct {
	Lister  Lister
	Quitter Quitter
}

// Sweep takes the current blocked-app list, queries running apps, and quits
// any matches. Matching is case-sensitive — that mirrors how macOS app names
// work (e.g. "Slack" not "slack").
func (m *Monitor) Sweep(blocked []string) ([]SweepResult, error) {
	if len(blocked) == 0 {
		return nil, nil
	}
	running, err := m.Lister.RunningApps()
	if err != nil {
		return nil, fmt.Errorf("list running apps: %w", err)
	}
	blockedSet := map[string]struct{}{}
	for _, b := range blocked {
		blockedSet[b] = struct{}{}
	}

	var out []SweepResult
	for _, app := range running {
		if _, hit := blockedSet[app]; !hit {
			continue
		}
		err := m.Quitter.Quit(app)
		out = append(out, SweepResult{App: app, Quit: err == nil, QuitErr: err})
	}
	return out, nil
}

// --- macOS implementations ---------------------------------------------------

// OSALister lists running apps via System Events.
type OSALister struct{}

const osaList = `tell application "System Events"
    set appList to name of every process whose background only is false
end tell
return appList`

func (OSALister) RunningApps() ([]string, error) {
	out, err := exec.Command("osascript", "-e", osaList).Output()
	if err != nil {
		return nil, fmt.Errorf("osascript list: %w", err)
	}
	parts := strings.Split(strings.TrimSpace(string(out)), ", ")
	for i, p := range parts {
		parts[i] = strings.TrimSpace(p)
	}
	return parts, nil
}

// OSAQuitter sends a quit AppleEvent to a named app.
type OSAQuitter struct{}

func (OSAQuitter) Quit(name string) error {
	// Escape any double quotes in the app name.
	safe := strings.ReplaceAll(name, `"`, `\"`)
	cmd := exec.Command("osascript", "-e",
		fmt.Sprintf(`tell application "%s" to quit`, safe))
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("osascript quit %q: %w (%s)", name, err, out)
	}
	return nil
}

// --- in-memory fakes for tests ----------------------------------------------

// FakeLister is a controllable Lister.
type FakeLister struct {
	mu   sync.Mutex
	Apps []string
	Err  error
}

func (f *FakeLister) RunningApps() ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return nil, f.Err
	}
	out := make([]string, len(f.Apps))
	copy(out, f.Apps)
	return out, nil
}

// FakeQuitter records quit calls and optionally returns errors per app.
type FakeQuitter struct {
	mu       sync.Mutex
	Quits    []string
	ErrorFor map[string]error
}

func (f *FakeQuitter) Quit(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Quits = append(f.Quits, name)
	if f.ErrorFor != nil {
		return f.ErrorFor[name]
	}
	return nil
}

// QuitsSnapshot returns a stable copy of recorded quits.
func (f *FakeQuitter) QuitsSnapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.Quits))
	copy(out, f.Quits)
	return out
}
