// Package appmon polls running macOS apps. Block targets are quit on sight;
// friction targets are quit and a friction screen is launched (so the user
// must pass a challenge before the cooldown elapses and the app is allowed
// to run undisturbed).
//
// The polling, quit, and friction-launch primitives are abstracted behind
// interfaces so the decision logic is testable on any platform.
package appmon

import (
	"fmt"
	"os/exec"
	"os/user"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ketan0/attend/internal/rules"
)

// Lister returns the names of currently-running user-visible apps.
type Lister interface {
	RunningApps() ([]string, error)
}

// Quitter requests an app exit by name.
type Quitter interface {
	Quit(name string) error
}

// FrictionLauncher displays a friction challenge for an app. The implementation
// is async by nature (a separate GUI app) and posts its result back to the
// daemon's HTTP API; this method returns once the launch is requested, not
// once the user finishes the challenge.
type FrictionLauncher interface {
	Launch(req rules.FrictionAppRequest) error
}

// SweepResult is returned for each blocked app encountered during a sweep.
type SweepResult struct {
	App     string
	Quit    bool
	QuitErr error
	// Friction is true when the sweep also triggered a friction launch
	// for this app (in addition to quitting it).
	Friction       bool
	FrictionErr    error
	FrictionRuleID string
}

// Monitor combines a Lister, Quitter, and (optional) FrictionLauncher.
type Monitor struct {
	Lister   Lister
	Quitter  Quitter
	Launcher FrictionLauncher

	// minLaunchInterval prevents the same friction window from being
	// re-launched while the user is still presumably looking at it. The
	// app gets quit on every sweep, but the friction screen is debounced.
	MinLaunchInterval time.Duration

	mu             sync.Mutex
	lastLaunchedAt map[string]time.Time // ruleID → last launch
}

// Sweep enacts the plan against currently-running apps. Block targets get
// Quit; friction targets get Quit + Launch (debounced + cooldown-respecting).
//
// Matching is case-sensitive on macOS app names ("Slack", not "slack").
func (m *Monitor) Sweep(blocked []string, friction []rules.FrictionAppRequest, settings rules.Settings, now time.Time) ([]SweepResult, error) {
	if len(blocked) == 0 && len(friction) == 0 {
		return nil, nil
	}
	running, err := m.Lister.RunningApps()
	if err != nil {
		return nil, fmt.Errorf("list running apps: %w", err)
	}
	runningSet := map[string]struct{}{}
	for _, a := range running {
		runningSet[a] = struct{}{}
	}

	var out []SweepResult

	// Hard blocks first.
	for _, b := range blocked {
		if _, ok := runningSet[b]; !ok {
			continue
		}
		qerr := m.Quitter.Quit(b)
		out = append(out, SweepResult{App: b, Quit: qerr == nil, QuitErr: qerr})
	}

	// Friction: quit + launch challenge.
	for _, f := range friction {
		if _, ok := runningSet[f.App]; !ok {
			continue
		}
		if settings.IsCooledDown(f.RuleID, now) {
			continue
		}
		res := SweepResult{App: f.App, FrictionRuleID: f.RuleID}
		qerr := m.Quitter.Quit(f.App)
		res.Quit = qerr == nil
		res.QuitErr = qerr

		if m.Launcher != nil && m.shouldLaunch(f.RuleID, now) {
			lerr := m.Launcher.Launch(f)
			res.Friction = lerr == nil
			res.FrictionErr = lerr
			if lerr == nil {
				m.markLaunched(f.RuleID, now)
			}
		}
		out = append(out, res)
	}
	return out, nil
}

func (m *Monitor) shouldLaunch(ruleID string, now time.Time) bool {
	interval := m.MinLaunchInterval
	if interval == 0 {
		interval = 30 * time.Second
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	last, ok := m.lastLaunchedAt[ruleID]
	return !ok || now.Sub(last) >= interval
}

func (m *Monitor) markLaunched(ruleID string, now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lastLaunchedAt == nil {
		m.lastLaunchedAt = map[string]time.Time{}
	}
	m.lastLaunchedAt[ruleID] = now
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

// LaunchctlFrictionLauncher spawns the AttendFriction GUI helper as the
// currently logged-in console user. The daemon runs as root (LaunchDaemon),
// so we can't render a window directly — `launchctl asuser <uid>` runs the
// helper inside the user's GUI session.
type LaunchctlFrictionLauncher struct {
	BinaryPath string // typically /usr/local/bin/AttendFriction
	DaemonURL  string // typically http://127.0.0.1:7723
}

func (l LaunchctlFrictionLauncher) Launch(req rules.FrictionAppRequest) error {
	uid, err := consoleUserUID()
	if err != nil {
		return fmt.Errorf("resolve console user: %w", err)
	}
	args := []string{
		"asuser", uid,
		l.BinaryPath,
		"--level", string(req.Friction.Level),
		"--target", req.App,
		"--challenge-id", req.RuleID,
		"--daemon-url", l.DaemonURL,
	}
	if req.Friction.TimerSeconds > 0 {
		args = append(args, "--timer-seconds", strconv.Itoa(req.Friction.TimerSeconds))
	}
	if req.Friction.Phrase != "" {
		args = append(args, "--phrase", req.Friction.Phrase)
	}
	cmd := exec.Command("launchctl", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl asuser: %w (%s)", err, out)
	}
	return nil
}

// consoleUserUID returns the UID of whoever owns /dev/console (the active
// GUI user). Returns an error if no one is logged in or the resolution fails.
func consoleUserUID() (string, error) {
	out, err := exec.Command("stat", "-f%Su", "/dev/console").Output()
	if err != nil {
		return "", err
	}
	name := strings.TrimSpace(string(out))
	if name == "" || name == "root" {
		return "", fmt.Errorf("no logged-in user")
	}
	u, err := user.Lookup(name)
	if err != nil {
		return "", err
	}
	return u.Uid, nil
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

// FakeLauncher records launch requests and optionally returns errors per rule.
type FakeLauncher struct {
	mu       sync.Mutex
	Launches []rules.FrictionAppRequest
	ErrorFor map[string]error // keyed by rule ID
}

func (f *FakeLauncher) Launch(req rules.FrictionAppRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Launches = append(f.Launches, req)
	if f.ErrorFor != nil {
		return f.ErrorFor[req.RuleID]
	}
	return nil
}

// LaunchesSnapshot returns a copy of recorded launches.
func (f *FakeLauncher) LaunchesSnapshot() []rules.FrictionAppRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]rules.FrictionAppRequest, len(f.Launches))
	copy(out, f.Launches)
	return out
}
