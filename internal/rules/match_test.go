package rules

import (
	"reflect"
	"testing"
	"time"
)

func mkRule(id string, action Action) Rule {
	return Rule{ID: id, Action: action}
}

func mkBlock(id, kind, value string) Rule {
	return Rule{
		ID: id, Action: ActionBlock,
		Target:   Target{Kind: TargetKind(kind), Value: value},
		Schedule: Schedule{Kind: ScheduleAlways},
	}
}

func mkAllow(id, kind, value string) Rule {
	return Rule{
		ID: id, Action: ActionAllow,
		Target:   Target{Kind: TargetKind(kind), Value: value},
		Schedule: Schedule{Kind: ScheduleAlways},
	}
}

func TestSystemBlocks_NoAllow(t *testing.T) {
	now := time.Now()
	rs := []Rule{
		mkBlock("r1", "domain", "twitter.com"),
		mkBlock("r2", "app", "Slack"),
	}
	domains, apps := SystemBlocks(rs, now)
	if !reflect.DeepEqual(domains, []string{"twitter.com"}) {
		t.Errorf("domains = %v", domains)
	}
	if !reflect.DeepEqual(apps, []string{"Slack"}) {
		t.Errorf("apps = %v", apps)
	}
}

func TestSystemBlocks_PathAllowNarrowsDomainBlock(t *testing.T) {
	now := time.Now()
	rs := []Rule{
		mkBlock("r1", "domain", "reddit.com"),
		mkAllow("r2", "path", "reddit.com/r/LocalLLaMA"),
	}
	domains, _ := SystemBlocks(rs, now)
	if len(domains) != 0 {
		t.Errorf("expected no domain blocks (allow narrows reddit.com), got %v", domains)
	}
}

func TestSystemBlocks_UnrelatedAllowDoesNotNarrow(t *testing.T) {
	now := time.Now()
	rs := []Rule{
		mkBlock("r1", "domain", "reddit.com"),
		mkAllow("r2", "path", "twitter.com/foo"),
	}
	domains, _ := SystemBlocks(rs, now)
	if !reflect.DeepEqual(domains, []string{"reddit.com"}) {
		t.Errorf("expected reddit.com still blocked, got %v", domains)
	}
}

func TestSystemBlocks_SameCanonicalAllowSuppressesBlock(t *testing.T) {
	// This shouldn't happen via the API (conflict detection), but the
	// helper should still handle it correctly.
	now := time.Now()
	rs := []Rule{
		mkBlock("r1", "domain", "twitter.com"),
		mkAllow("r2", "domain", "twitter.com"),
	}
	domains, _ := SystemBlocks(rs, now)
	if len(domains) != 0 {
		t.Errorf("expected no domain blocks (same-canonical allow), got %v", domains)
	}
}

func TestSystemBlocks_InactiveScheduleSkipped(t *testing.T) {
	now := time.Now()
	past := now.Add(-time.Hour)
	rs := []Rule{
		{
			ID: "r1", Action: ActionBlock,
			Target:   Target{Kind: TargetDomain, Value: "twitter.com"},
			Schedule: Schedule{Kind: ScheduleUntil, Until: &past},
		},
	}
	domains, _ := SystemBlocks(rs, now)
	if len(domains) != 0 {
		t.Errorf("expected expired rule not to block, got %v", domains)
	}
}

func TestSystemBlocks_InactiveAllowDoesNotSuppress(t *testing.T) {
	now := time.Now()
	past := now.Add(-time.Hour)
	rs := []Rule{
		mkBlock("r1", "domain", "reddit.com"),
		{
			ID: "r2", Action: ActionAllow,
			Target:   Target{Kind: TargetPath, Value: "reddit.com/r/x"},
			Schedule: Schedule{Kind: ScheduleUntil, Until: &past},
		},
	}
	domains, _ := SystemBlocks(rs, now)
	if !reflect.DeepEqual(domains, []string{"reddit.com"}) {
		t.Errorf("expected expired allow to NOT suppress block, got %v", domains)
	}
}

func TestSystemBlocks_PathAllowOnSubdomainStillNarrows(t *testing.T) {
	// "reddit.com/r/x" — host is "reddit.com" — narrows reddit.com block.
	// "old.reddit.com/r/x" — host is "old.reddit.com" — does NOT narrow
	// reddit.com (it's a different host string in the allow's target).
	now := time.Now()
	rs := []Rule{
		mkBlock("r1", "domain", "reddit.com"),
		mkAllow("r2", "path", "old.reddit.com/r/x"),
	}
	domains, _ := SystemBlocks(rs, now)
	if !reflect.DeepEqual(domains, []string{"reddit.com"}) {
		t.Errorf("subdomain path-allow should not narrow apex domain block, got %v", domains)
	}
}

func TestSystemBlocks_DedupsAndSorts(t *testing.T) {
	now := time.Now()
	rs := []Rule{
		mkBlock("r1", "domain", "twitter.com"),
		mkBlock("r2", "domain", "twitter.com"), // would be a conflict in practice
		mkBlock("r3", "domain", "x.com"),
	}
	domains, _ := SystemBlocks(rs, now)
	if !reflect.DeepEqual(domains, []string{"twitter.com", "x.com"}) {
		t.Errorf("expected sorted+deduped, got %v", domains)
	}
}

func TestPickEffective_Empty(t *testing.T) {
	if r := PickEffective(nil); r != nil {
		t.Errorf("expected nil, got %+v", r)
	}
}

func TestPickEffective_AllowWinsOverBlock(t *testing.T) {
	r := PickEffective([]Rule{mkRule("a", ActionBlock), mkRule("b", ActionAllow)})
	if r == nil || r.ID != "b" {
		t.Errorf("expected allow to win, got %+v", r)
	}
}

func TestPickEffective_AllowWinsOverFriction(t *testing.T) {
	r := PickEffective([]Rule{mkRule("a", ActionFriction), mkRule("b", ActionAllow)})
	if r == nil || r.ID != "b" {
		t.Errorf("expected allow to win, got %+v", r)
	}
}

func TestPickEffective_BlockOverFriction(t *testing.T) {
	r := PickEffective([]Rule{mkRule("a", ActionFriction), mkRule("b", ActionBlock)})
	if r == nil || r.ID != "b" {
		t.Errorf("expected block to win over friction, got %+v", r)
	}
}

func TestPickEffective_FrictionOverNudge(t *testing.T) {
	r := PickEffective([]Rule{mkRule("a", ActionNudge), mkRule("b", ActionFriction)})
	if r == nil || r.ID != "b" {
		t.Errorf("expected friction to win over nudge, got %+v", r)
	}
}

func TestPickEffective_SingleAllow(t *testing.T) {
	r := PickEffective([]Rule{mkRule("a", ActionAllow)})
	if r == nil || r.Action != ActionAllow {
		t.Errorf("expected allow, got %+v", r)
	}
}

func TestPickEffective_OrderIndependence(t *testing.T) {
	// Allow should win regardless of position in the list.
	r := PickEffective([]Rule{
		mkRule("a", ActionAllow),
		mkRule("b", ActionBlock),
		mkRule("c", ActionFriction),
	})
	if r.ID != "a" {
		t.Errorf("expected allow to win when first, got %s", r.ID)
	}
	r = PickEffective([]Rule{
		mkRule("a", ActionFriction),
		mkRule("b", ActionBlock),
		mkRule("c", ActionAllow),
	})
	if r.ID != "c" {
		t.Errorf("expected allow to win when last, got %s", r.ID)
	}
}
