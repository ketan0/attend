package rules

import "testing"

func TestFindConflict(t *testing.T) {
	existing := []Rule{
		{ID: "r1", Target: Target{Kind: TargetDomain, Value: "twitter.com"}},
		{ID: "r2", Target: Target{Kind: TargetApp, Value: "Slack"}},
		{ID: "r3", Target: Target{Kind: TargetPath, Value: "reddit.com/r/all"}},
	}

	t.Run("no conflict for new target", func(t *testing.T) {
		incoming := Rule{ID: "r9", Target: Target{Kind: TargetDomain, Value: "facebook.com"}}
		if c := FindConflict(incoming, existing, ""); c != nil {
			t.Errorf("expected no conflict, got %+v", c)
		}
	})

	t.Run("conflict on identical target", func(t *testing.T) {
		incoming := Rule{ID: "r9", Target: Target{Kind: TargetDomain, Value: "Twitter.com"}}
		c := FindConflict(incoming, existing, "")
		if c == nil {
			t.Fatalf("expected conflict")
		}
		if c.ExistingID != "r1" {
			t.Errorf("ExistingID = %q, want r1", c.ExistingID)
		}
	})

	t.Run("conflict ignored when replacing", func(t *testing.T) {
		incoming := Rule{ID: "r1", Target: Target{Kind: TargetDomain, Value: "twitter.com"}}
		if c := FindConflict(incoming, existing, "r1"); c != nil {
			t.Errorf("expected no conflict when replacing, got %+v", c)
		}
	})

	t.Run("domain vs path are not conflicts", func(t *testing.T) {
		// Reddit homepage block + path-level rule on /r/all are independent.
		incoming := Rule{ID: "r9", Target: Target{Kind: TargetDomain, Value: "reddit.com"}}
		if c := FindConflict(incoming, existing, ""); c != nil {
			t.Errorf("domain twitter.com should not conflict with path on reddit, got %+v", c)
		}
	})

	t.Run("app target conflicts case-sensitively", func(t *testing.T) {
		// Apps preserve case.
		incoming := Rule{ID: "r9", Target: Target{Kind: TargetApp, Value: "slack"}}
		if c := FindConflict(incoming, existing, ""); c != nil {
			t.Errorf("app slack vs Slack should not conflict (different macOS app names), got %+v", c)
		}
		incoming.Target.Value = "Slack"
		if c := FindConflict(incoming, existing, ""); c == nil {
			t.Errorf("app Slack should conflict with existing Slack rule")
		}
	})
}
