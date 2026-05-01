// Package rules defines the core rule data model and pure operations on it.
//
// Everything in this package is pure (no I/O, no time.Now() except where the
// caller passes a clock) so it can be exhaustively unit tested. Storage,
// enforcement, and the HTTP API are layered on top.
package rules

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Action is what a rule does when its target is engaged.
type Action string

const (
	ActionBlock    Action = "block"
	ActionFriction Action = "friction"
	ActionNudge    Action = "nudge"
	// ActionAllow carves an exception out of broader rules. When evaluating
	// a URL or app, if any allow rule matches, all other matching rules are
	// suppressed — even if they would otherwise block. Useful for the
	// "block reddit.com, allow reddit.com/r/LocalLLaMA" pattern.
	ActionAllow Action = "allow"
)

func (a Action) Valid() bool {
	switch a {
	case ActionBlock, ActionFriction, ActionNudge, ActionAllow:
		return true
	}
	return false
}

// TargetKind classifies what the rule applies to.
type TargetKind string

const (
	TargetDomain TargetKind = "domain" // e.g. "twitter.com"
	TargetPath   TargetKind = "path"   // e.g. "reddit.com/" or "reddit.com/r/all"
	TargetApp    TargetKind = "app"    // e.g. "Slack" (matches macOS app name)
)

func (k TargetKind) Valid() bool {
	switch k {
	case TargetDomain, TargetPath, TargetApp:
		return true
	}
	return false
}

// Target identifies what a rule acts on.
type Target struct {
	Kind  TargetKind `json:"kind"`
	Value string     `json:"value"`
}

// Canonical returns a normalized string used for equality and conflict checks.
func (t Target) Canonical() string {
	v := strings.TrimSpace(t.Value)
	switch t.Kind {
	case TargetDomain, TargetPath:
		v = strings.ToLower(v)
		v = strings.TrimPrefix(v, "http://")
		v = strings.TrimPrefix(v, "https://")
	}
	return string(t.Kind) + ":" + v
}

// FrictionLevel describes the kind of challenge interposed.
type FrictionLevel string

const (
	FrictionTimer  FrictionLevel = "timer"  // wait N seconds
	FrictionIntent FrictionLevel = "intent" // type why you're opening this (free text)
	FrictionPhrase FrictionLevel = "phrase" // type a specific phrase verbatim
	FrictionMath   FrictionLevel = "math"   // solve an arithmetic problem
	FrictionBreath FrictionLevel = "breath" // breathing exercise countdown
)

func (l FrictionLevel) Valid() bool {
	switch l {
	case FrictionTimer, FrictionIntent, FrictionPhrase, FrictionMath, FrictionBreath:
		return true
	}
	return false
}

// FrictionConfig parameterizes how a friction rule challenges the user.
type FrictionConfig struct {
	Level    FrictionLevel `json:"level"`
	Cooldown Duration      `json:"cooldown"` // how long a passed challenge stays valid
	// Level-specific (optional, defaults filled in by the engine):
	TimerSeconds int    `json:"timer_seconds,omitempty"` // for timer/breath
	Phrase       string `json:"phrase,omitempty"`        // for phrase
}

// ScheduleKind tags which schedule mode is in use.
type ScheduleKind string

const (
	ScheduleAlways    ScheduleKind = "always"
	ScheduleUntil     ScheduleKind = "until"
	ScheduleRecurring ScheduleKind = "recurring"
)

// Schedule says when a rule is active. Exactly one of Until / Recurring is
// populated, depending on Kind.
type Schedule struct {
	Kind      ScheduleKind       `json:"kind"`
	Until     *time.Time         `json:"until,omitempty"`     // RFC3339 hard end
	Recurring *RecurringSchedule `json:"recurring,omitempty"` // weekly windows
}

// RecurringSchedule expresses a set of weekly time windows in a fixed timezone.
type RecurringSchedule struct {
	Tz      string   `json:"tz"` // IANA tz, e.g. "America/Los_Angeles"
	Windows []Window `json:"windows"`
}

// Window is a single weekly time window. Days are lowercase 3-letter weekdays
// ("mon", "tue", ...). Start/End are "HH:MM" 24-hour strings. If End <= Start
// the window crosses midnight.
type Window struct {
	Days  []string `json:"days"`
	Start string   `json:"start"`
	End   string   `json:"end"`
}

// Settings holds daemon-wide state that isn't a rule.
type Settings struct {
	// PausedUntil, when non-nil, suppresses all enforcement until that
	// instant. Nil means not paused.
	PausedUntil *time.Time `json:"paused_until,omitempty"`
}

// IsPaused reports whether settings says we're currently paused at time t.
func (s Settings) IsPaused(t time.Time) bool {
	return s.PausedUntil != nil && t.Before(*s.PausedUntil)
}

// Rule is the unit of attention design.
type Rule struct {
	ID        string          `json:"id"`
	Action    Action          `json:"action"`
	Target    Target          `json:"target"`
	Schedule  Schedule        `json:"schedule"`
	Friction  *FrictionConfig `json:"friction,omitempty"`
	Message   string          `json:"message,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// Validate returns nil if the rule is well-formed, or an error explaining the
// first problem encountered. It does not consult external state.
func (r *Rule) Validate() error {
	if r.ID == "" {
		return fmt.Errorf("rule id is required")
	}
	if !r.Action.Valid() {
		return fmt.Errorf("invalid action %q", r.Action)
	}
	if !r.Target.Kind.Valid() {
		return fmt.Errorf("invalid target kind %q", r.Target.Kind)
	}
	if strings.TrimSpace(r.Target.Value) == "" {
		return fmt.Errorf("target value is required")
	}
	if r.Action == ActionFriction {
		if r.Friction == nil {
			return fmt.Errorf("friction rule requires friction config")
		}
		if !r.Friction.Level.Valid() {
			return fmt.Errorf("invalid friction level %q", r.Friction.Level)
		}
	} else if r.Friction != nil {
		return fmt.Errorf("friction config only allowed for friction action")
	}
	if r.Action == ActionNudge && strings.TrimSpace(r.Message) == "" {
		return fmt.Errorf("nudge rule requires message")
	}
	if r.Action == ActionAllow && r.Friction != nil {
		return fmt.Errorf("allow rule must not have friction config")
	}
	return r.Schedule.Validate()
}

// Validate reports schedule structural errors.
func (s Schedule) Validate() error {
	switch s.Kind {
	case ScheduleAlways:
		if s.Until != nil || s.Recurring != nil {
			return fmt.Errorf("schedule kind=always must not set until/recurring")
		}
	case ScheduleUntil:
		if s.Until == nil {
			return fmt.Errorf("schedule kind=until requires until timestamp")
		}
		if s.Recurring != nil {
			return fmt.Errorf("schedule kind=until must not set recurring")
		}
	case ScheduleRecurring:
		if s.Recurring == nil {
			return fmt.Errorf("schedule kind=recurring requires recurring config")
		}
		if s.Until != nil {
			return fmt.Errorf("schedule kind=recurring must not set until")
		}
		if err := s.Recurring.Validate(); err != nil {
			return err
		}
	default:
		return fmt.Errorf("invalid schedule kind %q", s.Kind)
	}
	return nil
}

// Validate reports recurring schedule errors.
func (r RecurringSchedule) Validate() error {
	if strings.TrimSpace(r.Tz) == "" {
		return fmt.Errorf("recurring.tz is required (IANA timezone)")
	}
	if _, err := time.LoadLocation(r.Tz); err != nil {
		return fmt.Errorf("invalid timezone %q: %w", r.Tz, err)
	}
	if len(r.Windows) == 0 {
		return fmt.Errorf("recurring.windows must contain at least one window")
	}
	for i, w := range r.Windows {
		if err := w.Validate(); err != nil {
			return fmt.Errorf("windows[%d]: %w", i, err)
		}
	}
	return nil
}

// Validate reports window errors.
func (w Window) Validate() error {
	if len(w.Days) == 0 {
		return fmt.Errorf("days is required")
	}
	for _, d := range w.Days {
		if !validDay(d) {
			return fmt.Errorf("invalid day %q (want mon|tue|wed|thu|fri|sat|sun)", d)
		}
	}
	if _, err := parseHM(w.Start); err != nil {
		return fmt.Errorf("invalid start %q: %w", w.Start, err)
	}
	if _, err := parseHM(w.End); err != nil {
		return fmt.Errorf("invalid end %q: %w", w.End, err)
	}
	return nil
}

// Duration is a time.Duration that JSON-encodes as a Go duration string
// ("2h", "30m", "5s"). Chosen because time.ParseDuration is universally
// available and unambiguous for agent callers.
type Duration time.Duration

func (d Duration) String() string { return time.Duration(d).String() }
func (d Duration) Std() time.Duration { return time.Duration(d) }

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(parsed)
	return nil
}

func validDay(s string) bool {
	switch strings.ToLower(s) {
	case "mon", "tue", "wed", "thu", "fri", "sat", "sun":
		return true
	}
	return false
}

func dayMatches(day string, wd time.Weekday) bool {
	switch strings.ToLower(day) {
	case "sun":
		return wd == time.Sunday
	case "mon":
		return wd == time.Monday
	case "tue":
		return wd == time.Tuesday
	case "wed":
		return wd == time.Wednesday
	case "thu":
		return wd == time.Thursday
	case "fri":
		return wd == time.Friday
	case "sat":
		return wd == time.Saturday
	}
	return false
}

// parseHM parses an "HH:MM" string into minutes since midnight.
func parseHM(s string) (int, error) {
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return 0, fmt.Errorf("expected HH:MM")
	}
	var h, m int
	if _, err := fmt.Sscanf(parts[0], "%d", &h); err != nil || h < 0 || h > 23 {
		return 0, fmt.Errorf("invalid hour")
	}
	if _, err := fmt.Sscanf(parts[1], "%d", &m); err != nil || m < 0 || m > 59 {
		return 0, fmt.Errorf("invalid minute")
	}
	return h*60 + m, nil
}
