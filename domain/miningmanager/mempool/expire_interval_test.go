package mempool

import (
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/domain/dagconfig"
)

// TestExpireIntervalIsAUnitConversion pins that a mempool lifetime expressed in seconds is turned
// into the number of blocks the network actually produces in that time.
//
// The old code divided by the block rate instead of multiplying by it. That is correct at exactly
// one block per second and wrong everywhere else, and it is why compounding transactions were being
// dropped from every mempool on the network within seconds of being submitted.
func TestExpireIntervalIsAUnitConversion(t *testing.T) {
	// A struct copy shares the slice's backing array, so overwriting elements in place would mutate
	// the global MainnetParams for every other test in the package. Replace the slice instead.
	params := dagconfig.MainnetParams
	params.TargetTimePerBlock = append([]time.Duration(nil), params.TargetTimePerBlock...)
	config := DefaultConfig(&params)

	tests := []struct {
		name               string
		targetTimePerBlock time.Duration
		wantDAAScore       uint64
	}{
		// Expressed against the constant rather than as bare numbers, so this keeps pinning the
		// conversion - multiply by the block rate, do not divide by it - if the window is retuned.
		// The one block per second case is the only one the old code got right.
		{"1 block per second", time.Second, defaultTransactionExpireIntervalSeconds * 1},
		// Five blocks per second is what block versions 5 and up target, and what the network runs.
		{"5 blocks per second", 200 * time.Millisecond, defaultTransactionExpireIntervalSeconds * 5},
		{"2 blocks per second", 500 * time.Millisecond, defaultTransactionExpireIntervalSeconds * 2},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for i := range params.TargetTimePerBlock {
				params.TargetTimePerBlock[i] = test.targetTimePerBlock
			}
			if got := config.transactionExpireIntervalDAAScore(); got != test.wantDAAScore {
				t.Errorf("expire interval = %d DAA score, want %d (%d seconds at %v per block)",
					got, test.wantDAAScore, config.TransactionExpireIntervalSeconds,
					test.targetTimePerBlock)
			}
		})
	}
}

// TestExpireIntervalFollowsTheLiveBlockVersion pins the second half of the fix. The block version is
// a process-global that starts at 1 and is only raised as blocks arrive, so converting once during
// startup froze the mempool on version 1's block rate no matter what the network moved to.
func TestExpireIntervalFollowsTheLiveBlockVersion(t *testing.T) {
	params := dagconfig.MainnetParams
	params.TargetTimePerBlock = []time.Duration{time.Second, 200 * time.Millisecond}
	config := DefaultConfig(&params)

	original := constants.GetBlockVersion()
	defer constants.SetBlockVersion(original)

	constants.SetBlockVersion(1)
	atVersion1 := config.transactionExpireIntervalDAAScore()

	constants.SetBlockVersion(2)
	atVersion2 := config.transactionExpireIntervalDAAScore()

	if want := defaultTransactionExpireIntervalSeconds * 1; atVersion1 != want {
		t.Errorf("at 1 block per second, expected %d DAA score, got %d", want, atVersion1)
	}
	if want := defaultTransactionExpireIntervalSeconds * 5; atVersion2 != want {
		t.Errorf("at 5 blocks per second, expected %d DAA score, got %d", want, atVersion2)
	}
	if atVersion1 == atVersion2 {
		t.Error("the interval must track the live block version, not the one seen at startup")
	}
}

// TestExpireIntervalNeverCollapsesToZero guards the degenerate case: a zero interval expires every
// transaction on the first scan.
func TestExpireIntervalNeverCollapsesToZero(t *testing.T) {
	params := dagconfig.MainnetParams
	config := DefaultConfig(&params)
	if got := config.secondsToDAAScore(0); got == 0 {
		t.Error("a zero interval would expire the whole mempool on the first scan")
	}
}
