package rules

import (
	"sort"
	"strings"
	"time"
)

// FrictionAppRequest is one app-level friction rule the daemon should
// enforce: when the named app is running and not in cooldown, the user gets
// a friction screen.
type FrictionAppRequest struct {
	RuleID   string         `json:"rule_id"`
	App      string         `json:"app"`
	Friction FrictionConfig `json:"friction"`
}

// SystemPlan is the full set of OS-level actions the daemon should take this
// tick. Returned by ComputeSystemPlan.
type SystemPlan struct {
	// Domains to sinkhole in /etc/hosts.
	Domains []string
	// Apps to quit on sight.
	BlockedApps []string
	// Apps to friction on sight (quit + show challenge).
	FrictionApps []FrictionAppRequest
}

// ComputeSystemPlan walks the rule list and produces the plan the daemon
// should enforce right now. Allow rules suppress block/friction rules with
// the same canonical target; path-allow rules under a domain block drop the
// domain from /etc/hosts so the browser extension can carve out paths.
//
// Pure: no I/O. Tests live in match_test.go.
func ComputeSystemPlan(rs []Rule, now time.Time) SystemPlan {
	canonAllow := map[string]struct{}{}
	domainNarrowed := map[string]struct{}{}
	for _, r := range rs {
		if !r.Schedule.IsActive(now) || r.Action != ActionAllow {
			continue
		}
		canonAllow[r.Target.Canonical()] = struct{}{}
		if r.Target.Kind == TargetPath {
			v := strings.ToLower(strings.TrimSpace(r.Target.Value))
			if i := strings.IndexByte(v, '/'); i >= 0 {
				v = v[:i]
			}
			if v != "" {
				domainNarrowed[v] = struct{}{}
			}
		}
	}

	domSet := map[string]struct{}{}
	blockedAppSet := map[string]struct{}{}
	var friction []FrictionAppRequest

	for _, r := range rs {
		if !r.Schedule.IsActive(now) {
			continue
		}
		if _, shadowed := canonAllow[r.Target.Canonical()]; shadowed {
			continue
		}
		switch r.Action {
		case ActionBlock:
			switch r.Target.Kind {
			case TargetDomain:
				v := strings.ToLower(strings.TrimSpace(r.Target.Value))
				if _, narrowed := domainNarrowed[v]; narrowed {
					continue
				}
				domSet[r.Target.Value] = struct{}{}
			case TargetApp:
				blockedAppSet[r.Target.Value] = struct{}{}
			}
		case ActionFriction:
			if r.Target.Kind == TargetApp && r.Friction != nil {
				friction = append(friction, FrictionAppRequest{
					RuleID:   r.ID,
					App:      r.Target.Value,
					Friction: *r.Friction,
				})
			}
		}
	}

	plan := SystemPlan{}
	for d := range domSet {
		plan.Domains = append(plan.Domains, d)
	}
	for a := range blockedAppSet {
		plan.BlockedApps = append(plan.BlockedApps, a)
	}
	plan.FrictionApps = friction
	sort.Strings(plan.Domains)
	sort.Strings(plan.BlockedApps)
	sort.Slice(plan.FrictionApps, func(i, j int) bool {
		return plan.FrictionApps[i].RuleID < plan.FrictionApps[j].RuleID
	})
	return plan
}

// SystemBlocks returns the domains and apps that should be blocked at the OS
// layer right now (i.e. what /etc/hosts and the app monitor should enforce).
// Allow rules suppress block rules:
//
//   - Same canonical target (rare in practice — conflict detection rejects
//     creating both without --replace).
//
//   - A path-allow whose host falls under a domain block. This is the
//     "block reddit.com, allow reddit.com/r/LocalLLaMA" case: the OS-level
//     block on reddit.com is dropped so the browser can load the page,
//     where the extension enforces block + allow at page-load time.
//
// Pure: no I/O, no clock except the passed-in `now`. Returns sorted,
// deduplicated slices.
func SystemBlocks(rs []Rule, now time.Time) (domains, apps []string) {
	canonAllow := map[string]struct{}{}
	domainNarrowed := map[string]struct{}{}

	for _, r := range rs {
		if !r.Schedule.IsActive(now) || r.Action != ActionAllow {
			continue
		}
		canonAllow[r.Target.Canonical()] = struct{}{}
		if r.Target.Kind == TargetPath {
			v := strings.ToLower(strings.TrimSpace(r.Target.Value))
			if i := strings.IndexByte(v, '/'); i >= 0 {
				v = v[:i]
			}
			if v != "" {
				domainNarrowed[v] = struct{}{}
			}
		}
	}

	domSet := map[string]struct{}{}
	appSet := map[string]struct{}{}
	for _, r := range rs {
		if !r.Schedule.IsActive(now) || r.Action != ActionBlock {
			continue
		}
		if _, shadowed := canonAllow[r.Target.Canonical()]; shadowed {
			continue
		}
		switch r.Target.Kind {
		case TargetDomain:
			v := strings.ToLower(strings.TrimSpace(r.Target.Value))
			if _, narrowed := domainNarrowed[v]; narrowed {
				continue
			}
			domSet[r.Target.Value] = struct{}{}
		case TargetApp:
			appSet[r.Target.Value] = struct{}{}
		}
	}

	for d := range domSet {
		domains = append(domains, d)
	}
	for a := range appSet {
		apps = append(apps, a)
	}
	sort.Strings(domains)
	sort.Strings(apps)
	return
}

// PickEffective walks rules and returns the one that should govern access to
// a target right now, or nil if no rule applies. Allow rules win — if any
// matching rule is `allow`, no enforcement happens. Otherwise the strictest
// matching action wins (block > friction > nudge).
//
// Pure: caller pre-filters `matching` to rules that match the target/URL.
// This function only handles precedence among already-matching rules.
func PickEffective(matching []Rule) *Rule {
	if len(matching) == 0 {
		return nil
	}
	for i := range matching {
		if matching[i].Action == ActionAllow {
			return &matching[i]
		}
	}
	priority := func(a Action) int {
		switch a {
		case ActionBlock:
			return 3
		case ActionFriction:
			return 2
		case ActionNudge:
			return 1
		}
		return 0
	}
	chosen := &matching[0]
	for i := 1; i < len(matching); i++ {
		if priority(matching[i].Action) > priority(chosen.Action) {
			chosen = &matching[i]
		}
	}
	return chosen
}
