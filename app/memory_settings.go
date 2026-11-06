package app

import (
	"fmt"
	"math"
	"runtime/metrics"
)

// EffectiveMemoryLimit reports the memory limit the runtime currently enforces. A negative argument
// to SetMemoryLimit returns the limit without changing it; math.MaxInt64 means "off".
func EffectiveMemoryLimit(setMemoryLimit func(int64) int64) string {
	limit := setMemoryLimit(-1)
	if limit == math.MaxInt64 {
		return "off"
	}
	return fmt.Sprintf("%d MB", limit/(1024*1024))
}

// EffectiveGCPercent reports the GC percent the runtime currently uses, or "off". It only reads the
// runtime metric; the runtime reports a disabled GC (a negative percent) as math.MaxUint64.
func EffectiveGCPercent() string {
	sample := []metrics.Sample{{Name: "/gc/gogc:percent"}}
	metrics.Read(sample)
	percent := sample[0].Value.Uint64()
	if percent == math.MaxUint64 {
		return "off"
	}
	return fmt.Sprintf("%d%%", percent)
}
