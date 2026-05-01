package rules

import "time"

// IsActive reports whether the rule's schedule is active at instant t.
// Pure: takes the clock time as a parameter so tests are deterministic.
func (s Schedule) IsActive(t time.Time) bool {
	switch s.Kind {
	case ScheduleAlways:
		return true
	case ScheduleUntil:
		if s.Until == nil {
			return false
		}
		return !t.After(*s.Until)
	case ScheduleRecurring:
		if s.Recurring == nil {
			return false
		}
		return s.Recurring.IsActive(t)
	}
	return false
}

// IsActive reports whether any window contains t (after converting t into the
// schedule's timezone).
func (r RecurringSchedule) IsActive(t time.Time) bool {
	loc, err := time.LoadLocation(r.Tz)
	if err != nil {
		return false
	}
	local := t.In(loc)
	for _, w := range r.Windows {
		if w.contains(local) {
			return true
		}
	}
	return false
}

// contains reports whether the window covers `local` (already in the schedule's
// timezone). Windows that cross midnight (End <= Start) wrap into the next day.
func (w Window) contains(local time.Time) bool {
	startMin, err := parseHM(w.Start)
	if err != nil {
		return false
	}
	endMin, err := parseHM(w.End)
	if err != nil {
		return false
	}
	nowMin := local.Hour()*60 + local.Minute()

	if endMin > startMin {
		// Same-day window. Active when current weekday matches a listed day
		// and time-of-day is in [start, end).
		if !w.dayListed(local.Weekday()) {
			return false
		}
		return nowMin >= startMin && nowMin < endMin
	}

	// Cross-midnight window: e.g. start=22:00 end=06:00 on "fri" means
	// Friday 22:00 → Saturday 06:00.
	// Two phases:
	//   Phase A: same calendar day as listed, time >= startMin
	//   Phase B: previous calendar day was listed, time < endMin
	if w.dayListed(local.Weekday()) && nowMin >= startMin {
		return true
	}
	prev := local.Weekday() - 1
	if prev < 0 {
		prev += 7
	}
	if w.dayListed(prev) && nowMin < endMin {
		return true
	}
	return false
}

func (w Window) dayListed(wd time.Weekday) bool {
	for _, d := range w.Days {
		if dayMatches(d, wd) {
			return true
		}
	}
	return false
}
