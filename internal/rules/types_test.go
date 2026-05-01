package rules

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestSettingsIsCooledDown(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	s := Settings{Cooldowns: map[string]time.Time{
		"r1": now.Add(5 * time.Minute),
		"r2": now.Add(-1 * time.Minute), // expired
	}}
	if !s.IsCooledDown("r1", now) {
		t.Errorf("r1 should be cooled down")
	}
	if s.IsCooledDown("r2", now) {
		t.Errorf("r2 should be expired")
	}
	if s.IsCooledDown("r3", now) {
		t.Errorf("r3 unknown should not be cooled down")
	}
}

func TestActionValid(t *testing.T) {
	cases := map[Action]bool{
		ActionBlock: true, ActionFriction: true, ActionNudge: true, ActionAllow: true,
		Action(""): false, Action("zap"): false,
	}
	for a, want := range cases {
		if got := a.Valid(); got != want {
			t.Errorf("Action(%q).Valid() = %v, want %v", a, got, want)
		}
	}
}

func TestTargetCanonical(t *testing.T) {
	cases := []struct {
		in   Target
		want string
	}{
		{Target{Kind: TargetDomain, Value: "Twitter.com"}, "domain:twitter.com"},
		{Target{Kind: TargetDomain, Value: "  HTTPS://Reddit.com "}, "domain:reddit.com"},
		{Target{Kind: TargetPath, Value: "Reddit.com/r/all"}, "path:reddit.com/r/all"},
		{Target{Kind: TargetApp, Value: "Slack"}, "app:Slack"}, // case preserved for apps
		{Target{Kind: TargetApp, Value: "  Slack  "}, "app:Slack"},
	}
	for _, c := range cases {
		if got := c.in.Canonical(); got != c.want {
			t.Errorf("%+v.Canonical() = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDurationRoundTrip(t *testing.T) {
	in := Duration(2*time.Hour + 30*time.Minute)
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(b) != `"2h30m0s"` {
		t.Errorf("marshal got %s", b)
	}
	var out Duration
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out != in {
		t.Errorf("roundtrip mismatch: got %v want %v", out, in)
	}
}

func TestDurationUnmarshalInvalid(t *testing.T) {
	var d Duration
	err := json.Unmarshal([]byte(`"banana"`), &d)
	if err == nil || !strings.Contains(err.Error(), "invalid duration") {
		t.Errorf("expected invalid duration error, got %v", err)
	}
}

func mustRule(t *testing.T, r Rule) {
	t.Helper()
	if err := r.Validate(); err != nil {
		t.Fatalf("expected valid, got %v", err)
	}
}

func mustErr(t *testing.T, r Rule, contains string) {
	t.Helper()
	err := r.Validate()
	if err == nil {
		t.Fatalf("expected error containing %q, got nil", contains)
	}
	if !strings.Contains(err.Error(), contains) {
		t.Fatalf("expected error containing %q, got %v", contains, err)
	}
}

func baseRule() Rule {
	return Rule{
		ID:       "r_test",
		Action:   ActionBlock,
		Target:   Target{Kind: TargetDomain, Value: "twitter.com"},
		Schedule: Schedule{Kind: ScheduleAlways},
	}
}

func TestRuleValidate(t *testing.T) {
	mustRule(t, baseRule())

	r := baseRule()
	r.ID = ""
	mustErr(t, r, "id is required")

	r = baseRule()
	r.Action = "weird"
	mustErr(t, r, "invalid action")

	r = baseRule()
	r.Target.Kind = "weird"
	mustErr(t, r, "invalid target kind")

	r = baseRule()
	r.Target.Value = "  "
	mustErr(t, r, "target value")

	r = baseRule()
	r.Action = ActionFriction
	mustErr(t, r, "friction rule requires friction config")

	r = baseRule()
	r.Friction = &FrictionConfig{Level: FrictionTimer}
	mustErr(t, r, "friction config only allowed for friction action")

	r = baseRule()
	r.Action = ActionFriction
	r.Friction = &FrictionConfig{Level: "weird"}
	mustErr(t, r, "invalid friction level")

	r = baseRule()
	r.Action = ActionNudge
	mustErr(t, r, "nudge rule requires message")

	// allow is a clean action with no extra requirements
	r = baseRule()
	r.Action = ActionAllow
	mustRule(t, r)

	// allow must not have friction config attached
	r = baseRule()
	r.Action = ActionAllow
	r.Friction = &FrictionConfig{Level: FrictionTimer}
	mustErr(t, r, "friction")
}

func TestScheduleValidate(t *testing.T) {
	now := time.Now()

	cases := []struct {
		name string
		s    Schedule
		ok   bool
		msg  string
	}{
		{"always-ok", Schedule{Kind: ScheduleAlways}, true, ""},
		{"always-with-until-bad", Schedule{Kind: ScheduleAlways, Until: &now}, false, "must not set"},
		{"until-ok", Schedule{Kind: ScheduleUntil, Until: &now}, true, ""},
		{"until-missing", Schedule{Kind: ScheduleUntil}, false, "requires until"},
		{"recurring-missing", Schedule{Kind: ScheduleRecurring}, false, "requires recurring"},
		{
			"recurring-ok",
			Schedule{
				Kind: ScheduleRecurring,
				Recurring: &RecurringSchedule{
					Tz: "America/Los_Angeles",
					Windows: []Window{{
						Days: []string{"mon"}, Start: "09:00", End: "17:00",
					}},
				},
			},
			true, "",
		},
		{
			"recurring-bad-tz",
			Schedule{
				Kind: ScheduleRecurring,
				Recurring: &RecurringSchedule{
					Tz: "Mars/Phobos",
					Windows: []Window{{Days: []string{"mon"}, Start: "09:00", End: "17:00"}},
				},
			},
			false, "invalid timezone",
		},
		{
			"recurring-bad-day",
			Schedule{
				Kind: ScheduleRecurring,
				Recurring: &RecurringSchedule{
					Tz: "UTC",
					Windows: []Window{{Days: []string{"funday"}, Start: "09:00", End: "17:00"}},
				},
			},
			false, "invalid day",
		},
		{
			"recurring-bad-time",
			Schedule{
				Kind: ScheduleRecurring,
				Recurring: &RecurringSchedule{
					Tz: "UTC",
					Windows: []Window{{Days: []string{"mon"}, Start: "9am", End: "17:00"}},
				},
			},
			false, "invalid start",
		},
		{"unknown-kind", Schedule{Kind: "wat"}, false, "invalid schedule kind"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.s.Validate()
			if c.ok {
				if err != nil {
					t.Fatalf("expected ok, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", c.msg)
			}
			if !strings.Contains(err.Error(), c.msg) {
				t.Fatalf("err = %v, want contains %q", err, c.msg)
			}
		})
	}
}
