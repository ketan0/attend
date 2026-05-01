package appmon

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/ketan0/attend/internal/rules"
)

func TestSweepNothingToDo(t *testing.T) {
	m := &Monitor{
		Lister:  &FakeLister{Apps: []string{"Slack"}},
		Quitter: &FakeQuitter{},
	}
	res, err := m.Sweep(nil, nil, rules.Settings{}, time.Now())
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(res) != 0 {
		t.Errorf("expected no results, got %v", res)
	}
}

func TestSweepQuitsBlocked(t *testing.T) {
	q := &FakeQuitter{}
	m := &Monitor{
		Lister:  &FakeLister{Apps: []string{"Slack", "Safari", "Twitter"}},
		Quitter: q,
	}
	res, err := m.Sweep([]string{"Slack", "Twitter"}, nil, rules.Settings{}, time.Now())
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(res) != 2 {
		t.Fatalf("expected 2 results, got %d (%+v)", len(res), res)
	}
	for _, r := range res {
		if !r.Quit || r.QuitErr != nil || r.Friction {
			t.Errorf("expected pure quit, got %+v", r)
		}
	}
}

func TestSweepCaseSensitive(t *testing.T) {
	q := &FakeQuitter{}
	m := &Monitor{
		Lister:  &FakeLister{Apps: []string{"Slack"}},
		Quitter: q,
	}
	res, _ := m.Sweep([]string{"slack"}, nil, rules.Settings{}, time.Now())
	if len(res) != 0 {
		t.Errorf("case mismatch should not match: %+v", res)
	}
}

func TestSweepRecordsQuitErrors(t *testing.T) {
	q := &FakeQuitter{ErrorFor: map[string]error{"Slack": errors.New("nope")}}
	m := &Monitor{
		Lister:  &FakeLister{Apps: []string{"Slack"}},
		Quitter: q,
	}
	res, err := m.Sweep([]string{"Slack"}, nil, rules.Settings{}, time.Now())
	if err != nil {
		t.Fatalf("Sweep returned err but should record per-app: %v", err)
	}
	if len(res) != 1 || res[0].Quit || res[0].QuitErr == nil {
		t.Errorf("expected one failed quit recorded, got %+v", res)
	}
}

func TestSweepListerError(t *testing.T) {
	m := &Monitor{
		Lister:  &FakeLister{Err: errors.New("boom")},
		Quitter: &FakeQuitter{},
	}
	if _, err := m.Sweep([]string{"Slack"}, nil, rules.Settings{}, time.Now()); err == nil {
		t.Errorf("expected lister error to propagate")
	}
}

// ---- friction-app behaviors ------------------------------------------------

func frictionReq(ruleID, app string) rules.FrictionAppRequest {
	return rules.FrictionAppRequest{
		RuleID:   ruleID,
		App:      app,
		Friction: rules.FrictionConfig{Level: rules.FrictionIntent},
	}
}

func TestSweepFrictionQuitsAndLaunches(t *testing.T) {
	q := &FakeQuitter{}
	l := &FakeLauncher{}
	m := &Monitor{
		Lister:   &FakeLister{Apps: []string{"Discord", "Safari"}},
		Quitter:  q,
		Launcher: l,
	}
	res, err := m.Sweep(nil, []rules.FrictionAppRequest{frictionReq("r1", "Discord")},
		rules.Settings{}, time.Now())
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(res) != 1 || !res[0].Quit || !res[0].Friction || res[0].FrictionRuleID != "r1" {
		t.Fatalf("expected quit+friction for Discord/r1, got %+v", res)
	}
	if !reflect.DeepEqual(q.QuitsSnapshot(), []string{"Discord"}) {
		t.Errorf("Quits = %v, want [Discord]", q.QuitsSnapshot())
	}
	launches := l.LaunchesSnapshot()
	if len(launches) != 1 || launches[0].App != "Discord" {
		t.Errorf("Launches = %+v, want one for Discord", launches)
	}
}

func TestSweepFrictionRespectsCooldown(t *testing.T) {
	q := &FakeQuitter{}
	l := &FakeLauncher{}
	m := &Monitor{
		Lister:   &FakeLister{Apps: []string{"Discord"}},
		Quitter:  q,
		Launcher: l,
	}
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	settings := rules.Settings{Cooldowns: map[string]time.Time{
		"r1": now.Add(5 * time.Minute),
	}}
	res, _ := m.Sweep(nil, []rules.FrictionAppRequest{frictionReq("r1", "Discord")},
		settings, now)
	if len(res) != 0 {
		t.Errorf("cooldown should suppress sweep, got %+v", res)
	}
	if len(q.QuitsSnapshot()) != 0 || len(l.LaunchesSnapshot()) != 0 {
		t.Errorf("expected no actions during cooldown")
	}
}

func TestSweepFrictionDebouncesLaunches(t *testing.T) {
	q := &FakeQuitter{}
	l := &FakeLauncher{}
	m := &Monitor{
		Lister:            &FakeLister{Apps: []string{"Discord"}},
		Quitter:           q,
		Launcher:          l,
		MinLaunchInterval: 30 * time.Second,
	}
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	req := []rules.FrictionAppRequest{frictionReq("r1", "Discord")}

	// First sweep: launch.
	_, _ = m.Sweep(nil, req, rules.Settings{}, now)
	// Second sweep 5s later: app re-running, but we should NOT relaunch.
	_, _ = m.Sweep(nil, req, rules.Settings{}, now.Add(5*time.Second))
	if got := len(l.LaunchesSnapshot()); got != 1 {
		t.Errorf("expected debounced to 1 launch, got %d", got)
	}
	// 31s later: debounce window expired.
	_, _ = m.Sweep(nil, req, rules.Settings{}, now.Add(31*time.Second))
	if got := len(l.LaunchesSnapshot()); got != 2 {
		t.Errorf("expected new launch after debounce window, got %d total", got)
	}
}

func TestSweepFrictionAppNotRunning(t *testing.T) {
	l := &FakeLauncher{}
	m := &Monitor{
		Lister:   &FakeLister{Apps: []string{"Safari"}}, // Discord not running
		Quitter:  &FakeQuitter{},
		Launcher: l,
	}
	_, _ = m.Sweep(nil, []rules.FrictionAppRequest{frictionReq("r1", "Discord")},
		rules.Settings{}, time.Now())
	if len(l.LaunchesSnapshot()) != 0 {
		t.Errorf("should not launch friction when app not running")
	}
}

func TestSweepFrictionWithNoLauncherStillQuits(t *testing.T) {
	q := &FakeQuitter{}
	m := &Monitor{
		Lister:   &FakeLister{Apps: []string{"Discord"}},
		Quitter:  q,
		Launcher: nil,
	}
	res, _ := m.Sweep(nil, []rules.FrictionAppRequest{frictionReq("r1", "Discord")},
		rules.Settings{}, time.Now())
	if len(res) != 1 || !res[0].Quit || res[0].Friction {
		t.Errorf("with nil launcher, should still quit; got %+v", res)
	}
}
