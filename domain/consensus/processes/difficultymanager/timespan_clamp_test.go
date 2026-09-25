package difficultymanager

import (
	"math/big"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
	"github.com/HoosatNetwork/HTND/v2/util/difficulty"
)

// TestRequiredDifficultyClampsAnAnomalousTimeSpan is the regression test for the mainnet difficulty
// collapse of 2026-09-19: a multi-hour node outage left a real, multi-hour timestamp gap inside the
// difficulty window's own min/max sample. The retarget formula (averageTarget * actualTimeSpan /
// expectedTimeSpan) had no bound on actualTimeSpan, so that one gap alone pushed the computed target
// straight to powMax - the easiest possible difficulty - regardless of what the window's own average
// target actually was. Once collapsed, recovery was gated on the window's bounded, blue-work-ranked
// sampling aging the gap out naturally, which can take far longer than the window size once collapsed,
// since a floor-difficulty block contributes proportionally far less blue work (CalcWork ~ 1/target)
// than the harder blocks it needs to displace.
//
// This pins that an anomalously large actualTimeSpan is now clamped to a bounded multiple of the
// window's own expected span before it can inflate the target, mirroring the kind of per-adjustment
// safety valve most PoW retarget algorithms use (e.g. Bitcoin's classic +-4x clamp).
func TestRequiredDifficultyClampsAnAnomalousTimeSpan(t *testing.T) {
	const targetTimePerBlockMs = 1000
	const windowSize = 3 // one entry is removed as the window's own min-timestamp block, leaving 2

	// A representative "real" average target: genesis-level, comfortably below powMax so both the
	// clamped and unclamped results below are distinguishable from powMax itself.
	genesisTarget := difficulty.CompactToBig(dagconfig.MainnetParams.GenesisBlock.Header.Bits())
	genesisBits := difficulty.BigToCompact(genesisTarget)

	dm := &difficultyManager{
		powMax:                         dagconfig.MainnetParams.PowMax,
		difficultyAdjustmentWindowSize: []int{windowSize},
		targetTimePerBlock:             []time.Duration{targetTimePerBlockMs * time.Millisecond},
		genesisBits:                    genesisBits,
	}

	newWindow := func(actualTimeSpanMs int64) blockWindow {
		return blockWindow{
			blocks: []difficultyBlock{
				{timeInMilliseconds: 0, bits: genesisBits},
				{timeInMilliseconds: actualTimeSpanMs, bits: genesisBits},
				{timeInMilliseconds: actualTimeSpanMs, bits: genesisBits},
			},
			minTimestamp:      0,
			maxTimestamp:      actualTimeSpanMs,
			minTimestampIndex: 0,
		}
	}

	// An anomalous gap: 100,000,000 seconds (~3 years) versus an expected span of 2 seconds
	// (targetTimePerBlockMs * (windowSize-1)). Enormously disproportionate on purpose, to guarantee
	// the unclamped computation blows past powMax (genesisTarget is already ~65536x below powMax on
	// mainnet, so the ratio here - 50,000,000x - overwhelms that headroom many times over).
	const anomalousSpanMs = 100_000_000_000

	bits, err := dm.requiredDifficultyFromTargetsWindow(newWindow(anomalousSpanMs), 1)
	if err != nil {
		t.Fatalf("requiredDifficultyFromTargetsWindow: %+v", err)
	}
	target := difficulty.CompactToBig(bits)

	powMaxBits := difficulty.BigToCompact(dagconfig.MainnetParams.PowMax)
	if bits == powMaxBits {
		t.Fatalf("expected the anomalous time span to be clamped before reaching powMax, but the result "+
			"is exactly powMax (%x) - the clamp did not apply", powMaxBits)
	}

	// The clamp bounds actualTimeSpan to at most maxTimeSpanMultiple times the expected span, so the
	// result must not exceed genesisTarget * maxTimeSpanMultiple (with headroom for integer rounding).
	const maxTimeSpanMultiple = 4
	upperBound := new(big.Int).Mul(genesisTarget, big.NewInt(maxTimeSpanMultiple+1))
	if target.Cmp(upperBound) > 0 {
		t.Fatalf("clamped target %s exceeds the expected upper bound %s (genesisTarget * %d)",
			target, upperBound, maxTimeSpanMultiple+1)
	}

	// And it must still reflect that the span really was too long relative to target - the result
	// should be easier (a larger target) than the window's own average target, just not unboundedly so.
	if target.Cmp(genesisTarget) <= 0 {
		t.Fatalf("expected the clamped target %s to still be easier than the window's average target %s",
			target, genesisTarget)
	}
}
