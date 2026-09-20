// Copyright (c) 2016 The btcsuite developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package dagconfig

import (
	"math"
	"strconv"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/constants"
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
	finalityDuration := 14400 * time.Second
	targetTimePerBlock := 250 * time.Millisecond
	var finalityDepth uint64
	if blockVersion < 5 {
		parsedFinalityDepth, err := strconv.ParseUint(strconv.FormatInt(int64(finalityDuration/targetTimePerBlock), 10), 10, 64)
		if err != nil {
			t.Fatalf("failed converting finality depth: %v", err)
		}
		finalityDepth = parsedFinalityDepth
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
	MergeSetSizeLimit := 10 * K
	var pruningDepth uint64
	if blockVersion < 5 {
		pruningDepth = 2*finalityDepth + 4*MergeSetSizeLimit*uint64(K) + 2*uint64(K) + 2
	} else {
		pruningDepth = 2*finalityDepth*PruningMultiplier + 4*MergeSetSizeLimit*uint64(K) + 2*uint64(K) + 2
	}
	t.Logf("PruningDepth %d", pruningDepth)
}

// TestPerVersionTablesAreClampedPastTheirEnd pins the HTN-217/HTN-226 hazard: the block version is a
// process-global one-way ratchet with no relation to the length of any of these tables, so reading
// one with a raw [version-1] index panics the moment a hard fork takes the version past the table's
// last entry. Every accessor here must clamp to the final entry instead, for any version, including
// versions far beyond what any current network defines.
func TestPerVersionTablesAreClampedPastTheirEnd(t *testing.T) {
	for _, params := range []*Params{&MainnetParams, &TestnetParams, &SimnetParams, &DevnetParams} {
		for _, blockVersion := range []uint16{0, 1, 5, 10, 11, 255, math.MaxUint16} {
			index := blockVersionIndexForSlice(len(params.K), blockVersion)
			if index < 0 || index >= len(params.K) {
				t.Fatalf("%s: K index %d for block version %d is outside the table (len %d)",
					params.Name, index, blockVersion, len(params.K))
			}
		}

		// The exported accessors read the process-global version, which cannot be moved backwards
		// from a test, so they are exercised at whatever it currently is - enough to catch an
		// accessor that indexes without clamping at all.
		if got := params.KForCurrentVersion(); got != params.K[blockVersionIndexForSlice(len(params.K), 1)] &&
			len(params.K) == 1 {
			t.Fatalf("%s: KForCurrentVersion returned %d for a single-entry table", params.Name, got)
		}
		_ = params.MaxBlockMassForCurrentVersion()
		_ = params.DifficultyAdjustmentWindowSizeForCurrentVersion()
		_ = params.TargetTimePerBlockForCurrentVersion()
	}
}

// TestPerVersionTablesCoverEveryActivatedVersion pins the other half: POWScores defines one more
// version than it has entries (version 1 is pre-activation), and every per-version table has to be
// extended in lockstep, or a node reaching the new version silently reuses the previous version's
// parameter instead of the one the fork intended.
func TestPerVersionTablesCoverEveryActivatedVersion(t *testing.T) {
	for _, params := range []*Params{&MainnetParams, &TestnetParams} {
		highestVersion := len(params.POWScores) + 1
		tables := map[string]int{
			"K":                              len(params.K),
			"TargetTimePerBlock":             len(params.TargetTimePerBlock),
			"FinalityDuration":               len(params.FinalityDuration),
			"DifficultyAdjustmentWindowSize": len(params.DifficultyAdjustmentWindowSize),
			"PruningMultiplier":              len(params.PruningMultiplier),
			"MaxBlockMass":                   len(params.MaxBlockMass),
			"MaxBlockParents":                len(params.MaxBlockParents),
			"MergeDepth":                     len(params.MergeDepth),
		}
		for name, length := range tables {
			if length < highestVersion {
				t.Errorf("%s: %s has %d entries but POWScores activates block versions up to %d - "+
					"a node on version %d would reuse entry %d instead of its own",
					params.Name, name, length, highestVersion, highestVersion, length-1)
			}
		}
	}
}

// TestForceSetBlockVersionPastEveryTableClampsRatherThanPanics drives the process-global block
// version past the end of every per-version table and then calls each accessor that reads it.
//
// This is the case HTN-217 was actually about. Two call sites (consensus factory.go's dagStores and
// the mempool's DefaultConfig) used to index a per-version table with constants.GetBlockVersion()-1
// and no bounds check at all, so the first node whose ambient version reached one past the table
// end died with an index-out-of-range rather than reusing the last entry. HTN-216's table extension
// only avoided that for one activation; without a clamp it recurs at every future fork.
//
// len(POWScores)+1 is the highest version a network can currently reach (version 1 is
// pre-activation), so +2 is deliberately one beyond even that: a clamp has to hold for a version
// that no activation table describes, which is exactly the state a node is in between shipping the
// gate and shipping the activation score.
//
// TestPerVersionTablesAreClampedPastTheirEnd covers blockVersionIndexForSlice directly; this covers
// the exported accessors at a global the other test cannot reach, since it deliberately never moves
// the global itself.
func TestForceSetBlockVersionPastEveryTableClampsRatherThanPanics(t *testing.T) {
	previousVersion := constants.GetBlockVersion()
	t.Cleanup(func() { constants.ForceSetBlockVersion(uint(previousVersion)) })

	for _, params := range []*Params{&MainnetParams, &TestnetParams, &SimnetParams, &DevnetParams} {
		// One past the longest per-version table, and never below len(POWScores)+2. Both bounds are
		// needed: on mainnet POWScores is the longest thing here, but on simnet the parameter
		// tables are far longer than POWScores, so len(POWScores)+2 there is an ordinary in-range
		// index and would assert nothing about clamping at all.
		beyondEveryTable := len(params.POWScores) + 2
		for _, length := range []int{
			len(params.K), len(params.TargetTimePerBlock), len(params.FinalityDuration),
			len(params.DifficultyAdjustmentWindowSize), len(params.PruningMultiplier),
			len(params.MaxBlockMass), len(params.MaxBlockParents), len(params.MergeDepth),
		} {
			if length+1 > beyondEveryTable {
				beyondEveryTable = length + 1
			}
		}
		constants.ForceSetBlockVersion(uint(beyondEveryTable))

		// A panic in any of these is the failure this test exists to catch, so they are simply
		// called. FinalityDepth and PruningDepth are included because they read the global through
		// their own path rather than through a *ForCurrentVersion helper.
		_ = params.TargetTimePerBlockForCurrentVersion()
		_ = params.DifficultyAdjustmentWindowSizeForCurrentVersion()
		_ = params.MaxBlockMassForCurrentVersion()
		_ = params.FinalityDepth()
		_ = params.PruningDepth()

		// Clamping specifically means "reuse the last entry", not "return some other entry" and not
		// "return a zero value" - a silent zero here would be a consensus parameter of 0.
		if got, want := params.KForCurrentVersion(), params.K[len(params.K)-1]; got != want {
			t.Errorf("%s: KForCurrentVersion at block version %d returned %d, want the last table "+
				"entry %d - the clamp did not reuse the final entry",
				params.Name, beyondEveryTable, got, want)
		}
		if got, want := params.MaxBlockMassForCurrentVersion(), params.MaxBlockMass[len(params.MaxBlockMass)-1]; got != want {
			t.Errorf("%s: MaxBlockMassForCurrentVersion at block version %d returned %d, want the "+
				"last table entry %d", params.Name, beyondEveryTable, got, want)
		}
		if got, want := params.TargetTimePerBlockForCurrentVersion(),
			params.TargetTimePerBlock[len(params.TargetTimePerBlock)-1]; got != want {
			t.Errorf("%s: TargetTimePerBlockForCurrentVersion at block version %d returned %s, want "+
				"the last table entry %s", params.Name, beyondEveryTable, got, want)
		}
		if got, want := params.DifficultyAdjustmentWindowSizeForCurrentVersion(),
			params.DifficultyAdjustmentWindowSize[len(params.DifficultyAdjustmentWindowSize)-1]; got != want {
			t.Errorf("%s: DifficultyAdjustmentWindowSizeForCurrentVersion at block version %d "+
				"returned %d, want the last table entry %d", params.Name, beyondEveryTable, got, want)
		}
	}
}
