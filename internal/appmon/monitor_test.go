package appmon

import (
	"errors"
	"testing"
)

func TestSweepNoBlocked(t *testing.T) {
	m := &Monitor{
		Lister:  &FakeLister{Apps: []string{"Slack", "Safari"}},
		Quitter: &FakeQuitter{},
	}
	res, err := m.Sweep(nil)
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
	res, err := m.Sweep([]string{"Slack", "Twitter"})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(res) != 2 {
		t.Fatalf("expected 2 results, got %d (%+v)", len(res), res)
	}
	for _, r := range res {
		if !r.Quit || r.QuitErr != nil {
			t.Errorf("expected quit ok, got %+v", r)
		}
	}
	got := q.QuitsSnapshot()
	if len(got) != 2 {
		t.Errorf("expected 2 quits, got %v", got)
	}
}

func TestSweepCaseSensitive(t *testing.T) {
	q := &FakeQuitter{}
	m := &Monitor{
		Lister:  &FakeLister{Apps: []string{"Slack"}},
		Quitter: q,
	}
	res, _ := m.Sweep([]string{"slack"})
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
	res, err := m.Sweep([]string{"Slack"})
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
	if _, err := m.Sweep([]string{"Slack"}); err == nil {
		t.Errorf("expected lister error to propagate")
	}
}
