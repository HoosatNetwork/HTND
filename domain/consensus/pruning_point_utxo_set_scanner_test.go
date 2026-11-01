package consensus

import (
	"testing"
	"time"
)

// A job waiting for disk space is retried after every block; its refusal is logged at
// most once per minute, and the end of the wait exactly once.
func TestDiskWaitLogRateLimit(t *testing.T) {
	start := time.Unix(1_700_000_000, 0)
	now := start
	w := diskWaitLog{now: func() time.Time { return now }}
	step := func(at time.Duration, refused, want bool) {
		t.Helper()
		now = start.Add(at)
		var got bool
		if refused {
			got = w.refused()
		} else {
			got = w.resumed()
		}
		if got != want {
			t.Fatalf("at %s (refused %t): logged %t, want %t", at, refused, got, want)
		}
	}
	step(0, true, true)                        // first refusal
	step(time.Second, true, false)             // every block
	step(59*time.Second, true, false)          // still inside the minute
	step(time.Minute, true, true)              // once per minute
	step(time.Minute+time.Second, true, false) // window restarts at the logged line
	step(2*time.Minute-time.Second, true, false)
	step(2*time.Minute, false, true) // resumed: one line
	step(2*time.Minute+time.Second, false, false)
	step(2*time.Minute+2*time.Second, true, true) // a new wait logs at once
}
