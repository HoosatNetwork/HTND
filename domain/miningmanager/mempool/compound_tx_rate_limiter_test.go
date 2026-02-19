package mempool

import (
	"testing"
	"time"

	"github.com/Hoosat-Oy/HTND/domain/dagconfig"
)

// Test that exactly MaxCompoundTxPerAddressPerMinute submissions within the 1-minute window
// cause the next (11th) to be rate-limited, and that when one falls out of the window,
// submissions are accepted again.
func TestCompoundTxRateLimiter_WindowAndLimit(t *testing.T) {
	cfg := DefaultConfig(&dagconfig.TestnetParams)
	cfg.CompoundTxRateLimitEnabled = true
	cfg.MaxCompoundTxPerAddressPerMinute = 10
	cfg.CompoundTxRateLimitWindowMinutes = 1

	rtl := newCompoundTxRateLimiter(cfg)
	addr := "hoosat:qptestaddressxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
	tracker := rtl.getOrCreateTracker(addr)

	base := time.Now()

	// Seed 10 submissions within the last minute
	tracker.mutex.Lock()
	for i := 0; i < int(cfg.MaxCompoundTxPerAddressPerMinute); i++ {
		tracker.submissions = append(tracker.submissions, compoundTxSubmission{
			timestamp: base.Add(-30*time.Second + time.Duration(i)*time.Second),
			txID:      "txid",
		})
	}
	tracker.mutex.Unlock()

	// After cleanup, all 10 remain within window
	rtl.cleanupOldSubmissions(tracker)

	if ok := rtl.checkRateLimit(addr); ok {
		t.Fatalf("expected address to be rate-limited with 10 submissions in window, but it was allowed")
	}

	// Move the oldest one beyond the 1-minute window
	tracker.mutex.Lock()
	if len(tracker.submissions) != int(cfg.MaxCompoundTxPerAddressPerMinute) {
		t.Fatalf("unexpected seeded submissions count: got %d, want %d", len(tracker.submissions), cfg.MaxCompoundTxPerAddressPerMinute)
	}
	tracker.submissions[0].timestamp = base.Add(-61 * time.Second)
	tracker.mutex.Unlock()

	rtl.cleanupOldSubmissions(tracker)

	if ok := rtl.checkRateLimit(addr); !ok {
		t.Fatalf("expected address to be allowed after one submission expired from the window, but it was rate-limited")
	}
}

// Test that recording with a past timestamp doesn't affect current window
func TestCompoundTxRateLimiter_RecordAtPastTime(t *testing.T) {
	cfg := DefaultConfig(&dagconfig.TestnetParams)
	cfg.CompoundTxRateLimitEnabled = true
	cfg.MaxCompoundTxPerAddressPerMinute = 10
	cfg.CompoundTxRateLimitWindowMinutes = 1

	rtl := newCompoundTxRateLimiter(cfg)
	addr := "hoosat:qptestaddressxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
	tracker := rtl.getOrCreateTracker(addr)

	// Record 10 submissions 2 minutes ago; they should be cleaned out and not count now
	past := time.Now().Add(-2 * time.Minute)
	tracker.mutex.Lock()
	for range 10 {
		tracker.submissions = append(tracker.submissions, compoundTxSubmission{timestamp: past, txID: "old"})
	}
	tracker.mutex.Unlock()

	rtl.cleanupOldSubmissions(tracker)
	if ok := rtl.checkRateLimit(addr); !ok {
		t.Fatalf("expected address to be allowed after past submissions expired, but it was rate-limited")
	}
}
