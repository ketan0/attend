package rules

// Conflict describes a clash between an incoming rule and an existing one.
type Conflict struct {
	ExistingID string
	Reason     string
}

// FindConflict returns a non-nil *Conflict if `incoming` clashes with any rule
// in `existing`. Pure: no I/O, no time. The "same canonical target" rule is
// the only conflict definition for v1 — overlapping but non-identical targets
// (e.g. domain twitter.com vs path twitter.com/foo) are *not* conflicts; they
// are independent rules with potentially overlapping enforcement, which is
// the user's intent.
//
// If `replacing` is non-empty, the existing rule with that ID is excluded
// from the conflict check (used for in-place updates / --replace).
func FindConflict(incoming Rule, existing []Rule, replacing string) *Conflict {
	canon := incoming.Target.Canonical()
	for _, e := range existing {
		if e.ID == replacing {
			continue
		}
		if e.Target.Canonical() == canon {
			return &Conflict{
				ExistingID: e.ID,
				Reason:     "a rule already targets " + canon,
			}
		}
	}
	return nil
}
