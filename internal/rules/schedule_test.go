package rules

import (
	"testing"
	"time"
)

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("LoadLocation %q: %v", name, err)
	}
	return loc
}

func TestScheduleAlways(t *testing.T) {
	s := Schedule{Kind: ScheduleAlways}
	for _, ts := range []time.Time{
		time.Now(),
		time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2099, 12, 31, 23, 59, 0, 0, time.UTC),
	} {
		if !s.IsActive(ts) {
			t.Errorf("always should be active at %v", ts)
		}
	}
}

func TestScheduleUntil(t *testing.T) {
	end := time.Date(2026, 5, 1, 18, 0, 0, 0, time.UTC)
	s := Schedule{Kind: ScheduleUntil, Until: &end}

	cases := map[time.Time]bool{
		end.Add(-time.Hour):   true,
		end:                   true,  // inclusive
		end.Add(time.Second):  false, // strictly after
		end.Add(-time.Second): true,
	}
	for ts, want := range cases {
		if got := s.IsActive(ts); got != want {
			t.Errorf("IsActive(%v) = %v, want %v", ts, got, want)
		}
	}
}

func TestRecurringSameDayWindow(t *testing.T) {
	la := mustLoc(t, "America/Los_Angeles")
	s := Schedule{
		Kind: ScheduleRecurring,
		Recurring: &RecurringSchedule{
			Tz: "America/Los_Angeles",
			Windows: []Window{
				{Days: []string{"mon", "tue", "wed", "thu", "fri"}, Start: "09:00", End: "17:00"},
			},
		},
	}
	cases := []struct {
		t    time.Time
		want bool
		desc string
	}{
		// Monday 2026-05-04 in LA
		{time.Date(2026, 5, 4, 8, 59, 0, 0, la), false, "mon 08:59 before window"},
		{time.Date(2026, 5, 4, 9, 0, 0, 0, la), true, "mon 09:00 start"},
		{time.Date(2026, 5, 4, 12, 0, 0, 0, la), true, "mon 12:00 mid"},
		{time.Date(2026, 5, 4, 16, 59, 0, 0, la), true, "mon 16:59 just before end"},
		{time.Date(2026, 5, 4, 17, 0, 0, 0, la), false, "mon 17:00 end exclusive"},
		// Saturday 2026-05-02 in LA
		{time.Date(2026, 5, 2, 12, 0, 0, 0, la), false, "sat 12:00 not listed"},
	}
	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			if got := s.IsActive(c.t); got != c.want {
				t.Errorf("IsActive(%v) = %v, want %v", c.t, got, c.want)
			}
		})
	}
}

func TestRecurringTzConversion(t *testing.T) {
	// Window is 09:00-10:00 LA time on Monday. A UTC instant of Mon 16:00
	// is Mon 09:00 LA → active.
	s := Schedule{
		Kind: ScheduleRecurring,
		Recurring: &RecurringSchedule{
			Tz:      "America/Los_Angeles",
			Windows: []Window{{Days: []string{"mon"}, Start: "09:00", End: "10:00"}},
		},
	}
	utcMon16 := time.Date(2026, 5, 4, 16, 0, 0, 0, time.UTC) // = 09:00 PDT
	if !s.IsActive(utcMon16) {
		t.Errorf("expected active at %v (= mon 09:00 PDT)", utcMon16)
	}
	utcMon14 := time.Date(2026, 5, 4, 14, 0, 0, 0, time.UTC) // = 07:00 PDT
	if s.IsActive(utcMon14) {
		t.Errorf("expected inactive at %v (= mon 07:00 PDT)", utcMon14)
	}
}

func TestRecurringCrossesMidnight(t *testing.T) {
	la := mustLoc(t, "America/Los_Angeles")
	// Friday 22:00 → Saturday 06:00
	s := Schedule{
		Kind: ScheduleRecurring,
		Recurring: &RecurringSchedule{
			Tz:      "America/Los_Angeles",
			Windows: []Window{{Days: []string{"fri"}, Start: "22:00", End: "06:00"}},
		},
	}
	// Friday 2026-05-01 LA
	fri := func(h, m int) time.Time { return time.Date(2026, 5, 1, h, m, 0, 0, la) }
	sat := func(h, m int) time.Time { return time.Date(2026, 5, 2, h, m, 0, 0, la) }

	cases := []struct {
		t    time.Time
		want bool
		desc string
	}{
		{fri(21, 59), false, "fri before window"},
		{fri(22, 0), true, "fri start"},
		{fri(23, 30), true, "fri mid"},
		{sat(0, 0), true, "sat midnight"},
		{sat(5, 59), true, "sat just before end"},
		{sat(6, 0), false, "sat end exclusive"},
		{sat(22, 0), false, "sat next eve unrelated"},
	}
	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			if got := s.IsActive(c.t); got != c.want {
				t.Errorf("IsActive(%v) = %v, want %v", c.t, got, c.want)
			}
		})
	}
}

func TestRecurringMultipleWindows(t *testing.T) {
	la := mustLoc(t, "America/Los_Angeles")
	s := Schedule{
		Kind: ScheduleRecurring,
		Recurring: &RecurringSchedule{
			Tz: "America/Los_Angeles",
			Windows: []Window{
				{Days: []string{"mon"}, Start: "09:00", End: "12:00"},
				{Days: []string{"mon"}, Start: "13:00", End: "17:00"},
			},
		},
	}
	mon := func(h, m int) time.Time { return time.Date(2026, 5, 4, h, m, 0, 0, la) }

	if s.IsActive(mon(12, 30)) {
		t.Errorf("expected inactive at lunch break")
	}
	if !s.IsActive(mon(10, 0)) {
		t.Errorf("expected active in morning window")
	}
	if !s.IsActive(mon(14, 0)) {
		t.Errorf("expected active in afternoon window")
	}
}
