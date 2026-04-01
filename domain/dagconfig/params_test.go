// Copyright (c) 2016 The btcsuite developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package dagconfig

import (
	"math"
	"testing"
	"time"

	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
)

func TestNewHashFromStr(t *testing.T) {
	tests := []struct {
		hexStr        string
		expectedHash  *externalapi.DomainHash
		expectedPanic bool
	}{
		{"banana", nil, true},
		{
			"0000000000000000000000000000000000000000000000000000000000000000",
			externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}),
			false,
		},
		{
			"0101010101010101010101010101010101010101010101010101010101010101",
			externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1}),
			false,
		},
	}

	for _, test := range tests {
		func() {
			defer func() {
				err := recover()
				if (err != nil) != test.expectedPanic {
					t.Errorf("%s: Expected panic: %t for invalid hash, got %t", test.hexStr, test.expectedPanic, err != nil)
				}
			}()

			result := newHashFromStr(test.hexStr)

			if !result.Equal(test.expectedHash) {
				t.Errorf("%s: Expected hash: %s, but got %s", test.hexStr, test.expectedHash, result)
			}
		}()
	}
}

// newHashFromStr converts the passed big-endian hex string into a externalapi.DomainHash.
// It only differs from the one available in hashes package in that it panics on an error
// since it will only be called from tests.
func newHashFromStr(hexStr string) *externalapi.DomainHash {
	hash, err := externalapi.NewDomainHashFromString(hexStr)
	if err != nil {
		panic(err)
	}
	return hash
}

// TestMustRegisterPanic ensures the mustRegister function panics when used to
// register an invalid network.
func TestMustRegisterPanic(t *testing.T) {
	t.Parallel()

	// Setup a defer to catch the expected panic to ensure it actually
	// paniced.
	defer func() {
		if err := recover(); err == nil {
			t.Error("mustRegister did not panic as expected")
		}
	}()

	// Intentionally try to register duplicate params to force a panic.
	mustRegister(&MainnetParams)
}

// TestSkipProofOfWork ensures all of the hard coded network params don't set SkipProofOfWork as true.
func TestSkipProofOfWork(t *testing.T) {
	allParams := []Params{
		MainnetParams,
		TestnetParams,
		SimnetParams,
		DevnetParams,
	}

	for _, params := range allParams {
		if params.SkipProofOfWork {
			t.Errorf("SkipProofOfWork is enabled for %s. This option should be "+
				"used only for tests.", params.Name)
		}
	}
}

// calculateK estimates the k value for GHOSTDAG based on blocks per second (bps).
// It uses a heuristic that scales k with bps, adjusts for network latency, and ensures
// security against a target hashrate attack (e.g., 47.5%).
// Parameters:
// - bps: Blocks per second (e.g., 1, 5, 10).
// - latencyMs: Network latency in milliseconds (e.g., 500 ms).
// - attackerHashrate: Attacker's hashrate fraction (e.g., 0.475 for 47.5%).
// - errorProb: Error probability for security (e.g., 0.01 for 99% confidence).
// Returns: Estimated k value (rounded up to the nearest integer).
func calculateK(bps float64, latencyMs float64, attackerHashrate float64, errorProb float64) int {
	// Base k value at 1 bps (Kaspa's current setting).
	const baseK = 18
	const baseBps = 1.0

	// Step 1: Security threshold based on GHOSTDAG whitepaper formula.
	// k >= ln(1/epsilon) / ln((1-p)/p), where p is attacker's hashrate fraction.
	securityK := math.Log(1/errorProb) / math.Log((1-attackerHashrate)/attackerHashrate)

	// Step 2: Scale k based on block rate to accommodate more parallel blocks.
	// Rough scaling: k_new = baseK * (bps / baseBps).
	bpsScaling := baseK * (bps / baseBps)

	// Step 3: Adjust for network latency.
	// Blocks in latency window = bps * (latencyMs / 1000).
	latencyBlocks := bps * (latencyMs / 1000)
	// k should be ~15x the number of blocks in the latency window to ensure honest blocks form a k-cluster.
	latencyK := latencyBlocks * 15

	// Step 4: Take the maximum of securityK, bpsScaling, and latencyK to ensure all constraints are met.
	estimatedK := math.Max(securityK, math.Max(bpsScaling, latencyK))

	// Step 5: Round up to the nearest integer and add a safety margin (e.g., 10%).
	return int(math.Ceil(estimatedK * 1.1))
}

// TestCalculateK tests the calculateK function for various bps values.
// It verifies that k values are within expected ranges based\left
func TestCalculateK(t *testing.T) {
	tests := []struct {
		name             string
		bps              float64
		latencyMs        float64
		attackerHashrate float64
		errorProb        float64
		expectedMinK     int
		expectedMaxK     int
	}{
		{
			name:             "1 bps (current Kaspa setting)",
			bps:              1.0,
			latencyMs:        500.0,
			attackerHashrate: 0.475,
			errorProb:        0.01,
			expectedMinK:     45,
			expectedMaxK:     55,
		},
		{
			name:             "5 bps",
			bps:              5.0,
			latencyMs:        500.0,
			attackerHashrate: 0.475,
			errorProb:        0.01,
			expectedMinK:     90,
			expectedMaxK:     110,
		},
		{
			name:             "10 bps",
			bps:              10.0,
			latencyMs:        500.0,
			attackerHashrate: 0.475,
			errorProb:        0.01,
			expectedMinK:     180,
			expectedMaxK:     220,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k := calculateK(tt.bps, tt.latencyMs, tt.attackerHashrate, tt.errorProb)
			if k < tt.expectedMinK || k > tt.expectedMaxK {
				t.Errorf("calculateK(bps=%.1f, latencyMs=%.1f, attackerHashrate=%.3f, errorProb=%.3f) = %d; expected between %d and %d",
					tt.bps, tt.latencyMs, tt.attackerHashrate, tt.errorProb, k, tt.expectedMinK, tt.expectedMaxK)
			}
		})
	}
}

func TestFinalityDepth(t *testing.T) {
	blockVersion := 5
	var finalityDuration time.Duration = 14400 * time.Second
	var targetTimePerBlock time.Duration = 250 * time.Millisecond
	var finalityDepth uint64
	if blockVersion < 5 {
		finalityDepth = uint64(finalityDuration / targetTimePerBlock)
	} else {
		finalityDepth = uint64(finalityDuration.Seconds() / targetTimePerBlock.Seconds())
	}
	t.Logf("FinalityDepth %d", finalityDepth)
}

// PruningDepth returns the pruning duration represented in blocks
func TestPruningDepth(t *testing.T) {
	blockVersion := 5
	var finalityDepth uint64 = 57600
	var PruningMultiplier uint64 = 3
	var K uint64 = 40
	var MergeSetSizeLimit uint64 = 10 * K
	var pruningDepth uint64
	if blockVersion < 5 {
		pruningDepth = 2*finalityDepth + 4*MergeSetSizeLimit*uint64(K) + 2*uint64(K) + 2
	} else {
		pruningDepth = 2*finalityDepth*PruningMultiplier + 4*MergeSetSizeLimit*uint64(K) + 2*uint64(K) + 2
	}
	t.Logf("PruningDepth %d", pruningDepth)
}
