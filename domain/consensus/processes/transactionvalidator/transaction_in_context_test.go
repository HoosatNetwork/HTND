package transactionvalidator

import (
	"math"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/txscript"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/utxo"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
)

// TestScriptFlagsFollowMLDSA44BlockVersion pins that ML-DSA-44 is enabled by the DAA score of the
// block being validated, through POWScores and MLDSA44SignaturesBlockVersion.
func TestScriptFlagsFollowMLDSA44BlockVersion(t *testing.T) {
	// Version 3 is reached at DAA score 200.
	params := dagconfig.Params{POWScores: []uint64{100, 200}, HardForkGates: dagconfig.HardForkGates{MLDSA44SignaturesBlockVersion: 3}}
	validator := transactionValidator{dagParams: &params}
	tests := []struct {
		daaScore   uint64
		wantActive bool
	}{
		{daaScore: 0, wantActive: false},
		{daaScore: 199, wantActive: false},
		{daaScore: 200, wantActive: true},
		{daaScore: 1_000_000, wantActive: true},
	}
	for _, test := range tests {
		flags := validator.scriptFlagsForDAAScore(test.daaScore)
		if gotActive := flags&txscript.ScriptEnableMLDSA44 != 0; gotActive != test.wantActive {
			t.Fatalf("DAA score %d: ML-DSA-44 active = %t, want %t", test.daaScore, gotActive, test.wantActive)
		}
	}
}

// TestMLDSA44FollowsNetworkGate checks the signature gate at every version each
// shipped schedule can produce. Versions are derived from POWScores, as in the
// block builder; GetBlockVersion only reflects blocks seen since startup.
func TestMLDSA44FollowsNetworkGate(t *testing.T) {
	for _, params := range []*dagconfig.Params{&dagconfig.MainnetParams, &dagconfig.TestnetParams,
		&dagconfig.TestnetParamsB5, &dagconfig.TestnetParamsB10, &dagconfig.SimnetParams, &dagconfig.DevnetParams} {
		t.Run(params.Name, func(t *testing.T) {
			highestVersion := constants.BlockVersionForDAAScore(params.POWScores, math.MaxUint64)
			for version := uint16(1); version <= highestVersion; version++ {
				wantActive := params.HardForkGates.MLDSA44SignaturesBlockVersion != 0 &&
					version >= params.HardForkGates.MLDSA44SignaturesBlockVersion
				if gotActive := params.MLDSA44SignaturesActive(version); gotActive != wantActive {
					t.Errorf("ML-DSA-44 active at block version %d = %t, want %t",
						version, gotActive, wantActive)
				}
			}
			validator := transactionValidator{dagParams: params}
			// Check the actual script flags at both sides of every scheduled fork.
			scores := []uint64{0, math.MaxUint64}
			for _, score := range params.POWScores {
				scores = append(scores, score)
				if score > 0 {
					scores = append(scores, score-1)
				}
			}
			for _, score := range scores {
				blockVersion := constants.BlockVersionForDAAScore(params.POWScores, score)
				wantActive := params.MLDSA44SignaturesActive(blockVersion)
				gotActive := validator.scriptFlagsForDAAScore(score)&txscript.ScriptEnableMLDSA44 != 0
				if gotActive != wantActive {
					t.Errorf("ML-DSA-44 script flag at DAA score %d = %t, want %t",
						score, gotActive, wantActive)
				}
			}
		})
	}
}

func TestNextForkScoresActivateMLDSA44AndDevFeeRemoval(t *testing.T) {
	tests := []struct {
		params       *dagconfig.Params
		score        uint64
		blockVersion uint16
	}{
		{params: &dagconfig.MainnetParams, score: 258_000_000, blockVersion: 12},
		{params: &dagconfig.TestnetParams, score: 10_000_000, blockVersion: 13},
	}

	for _, test := range tests {
		params := test.params
		if got := constants.BlockVersionForDAAScore(params.POWScores, test.score-1); got != test.blockVersion-1 {
			t.Errorf("%s: block version immediately before DAA score %d is %d, want %d",
				params.Name, test.score, got, test.blockVersion-1)
		}
		if got := constants.BlockVersionForDAAScore(params.POWScores, test.score); got != test.blockVersion {
			t.Errorf("%s: block version at DAA score %d is %d, want %d",
				params.Name, test.score, got, test.blockVersion)
		}
		if params.HardForkGates.MLDSA44SignaturesBlockVersion != test.blockVersion {
			t.Errorf("%s: ML-DSA-44 gate is %d, want %d",
				params.Name, params.HardForkGates.MLDSA44SignaturesBlockVersion, test.blockVersion)
		}
		if params.HardForkGates.DevFeeRemovalBlockVersion != test.blockVersion {
			t.Errorf("%s: dev-fee removal gate is %d, want %d",
				params.Name, params.HardForkGates.DevFeeRemovalBlockVersion, test.blockVersion)
		}
	}
}

// TestSequenceLocksActive tests the SequenceLockActive function to ensure it
// works as expected in all possible combinations/scenarios.
func TestSequenceLocksActive(t *testing.T) {
	tests := []struct {
		seqLock       sequenceLock
		blockDAAScore uint64

		want bool
	}{
		// Block based sequence lock with equal block DAA score.
		{seqLock: sequenceLock{1000}, blockDAAScore: 1001, want: true},

		// Block based sequence lock with current DAA score below seq lock block DAA score.
		{seqLock: sequenceLock{1000}, blockDAAScore: 90, want: false},

		// Block based sequence lock at the same DAA score, so shouldn't yet be active.
		{seqLock: sequenceLock{1000}, blockDAAScore: 1000, want: false},
	}

	validator := transactionValidator{}
	for i, test := range tests {
		got := validator.sequenceLockActive(&test.seqLock, test.blockDAAScore)
		if got != test.want {
			t.Fatalf("SequenceLockActive #%d got %v want %v", i, got, test.want)
		}
	}
}

func TestCalcTxSequenceLockFromReferencedUTXOEntriesIgnoresUnacceptedDAAScore(t *testing.T) {
	validator := transactionValidator{}
	tx := &externalapi.DomainTransaction{
		Inputs: []*externalapi.DomainTransactionInput{{
			Sequence: 1,
			UTXOEntry: utxo.NewUTXOEntry(
				1,
				&externalapi.ScriptPublicKey{},
				false,
				constants.UnacceptedDAAScore,
			),
		}},
	}

	sequenceLock, err := validator.calcTxSequenceLockFromReferencedUTXOEntries(nil, nil, tx)
	if err != nil {
		t.Fatalf("calcTxSequenceLockFromReferencedUTXOEntries: %+v", err)
	}
	if sequenceLock.BlockDAAScore != -1 {
		t.Fatalf("unexpected sequence lock block DAA score: got %d want -1", sequenceLock.BlockDAAScore)
	}
}
