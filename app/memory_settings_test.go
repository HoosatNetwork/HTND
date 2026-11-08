package app

import (
	"math"
	"runtime/debug"
	"runtime/metrics"
	"sync/atomic"
	"testing"
)

// TestEffectiveGCPercent checks the reported GC percent for off, zero and an explicit value, and that
// reading it leaves the runtime's GC percent unchanged.
func TestEffectiveGCPercent(t *testing.T) {
	tests := []struct {
		percent int
		want    string
	}{
		{-1, "off"},
		{0, "0%"},
		{75, "75%"},
	}
	for _, test := range tests {
		t.Run(test.want, func(t *testing.T) {
			defer debug.SetGCPercent(debug.SetGCPercent(test.percent))
			if got := EffectiveGCPercent(); got != test.want {
				t.Errorf("EffectiveGCPercent() = %q, want %q", got, test.want)
			}
			if after := debug.SetGCPercent(test.percent); after != test.percent {
				t.Errorf("GC percent after the read = %d, want %d", after, test.percent)
			}
		})
	}
}

// TestEffectiveGCPercentNeverDisablesGC checks that reading the GC percent does not change it even
// for a moment: a concurrent observer must never see the GC disabled while it is enabled.
func TestEffectiveGCPercentNeverDisablesGC(t *testing.T) {
	defer debug.SetGCPercent(debug.SetGCPercent(100))
	var stop atomic.Bool
	var seenOff atomic.Int64
	done := make(chan struct{})
	go func() {
		defer close(done)
		sample := []metrics.Sample{{Name: "/gc/gogc:percent"}}
		for !stop.Load() {
			metrics.Read(sample)
			if sample[0].Value.Uint64() == math.MaxUint64 {
				seenOff.Add(1)
			}
		}
	}()
	for i := 0; i < 1000; i++ {
		EffectiveGCPercent()
	}
	stop.Store(true)
	<-done
	if n := seenOff.Load(); n > 0 {
		t.Errorf("a concurrent observer saw the GC disabled %d times during the reads", n)
	}
}
