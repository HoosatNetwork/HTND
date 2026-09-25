package app

import "testing"

// A streak of disqualified blocks can be mined by anyone on top of the tip, so stopping on it is
// opt-in: without --stop-on-disqualified-streak consensus only logs it.
func TestDisqualifiedBlockStreakStopIsOptIn(t *testing.T) {
	if disqualifiedBlockStreakHandler(false) != nil {
		t.Fatalf("the node must not be wired to stop on a disqualified streak by default")
	}
	if disqualifiedBlockStreakHandler(true) == nil {
		t.Fatalf("--stop-on-disqualified-streak must wire the stop")
	}
}
