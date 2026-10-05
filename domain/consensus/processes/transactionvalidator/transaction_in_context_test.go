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

// TestMLDSA44IsInertOnEveryNetwork pins that version 11 is out of reach of every network's
// POWScores today, so shipping this code changes no block's validity until an activation DAA
// score is added.
func TestMLDSA44IsInertOnEveryNetwork(t *testing.T) {
	for _, params := range []*dagconfig.Params{&dagconfig.MainnetParams, &dagconfig.TestnetParams,
		&dagconfig.SimnetParams, &dagconfig.DevnetParams} {
		if params.HardForkGates.MLDSA44SignaturesBlockVersion != 11 {
			t.Fatalf("%s: MLDSA44SignaturesBlockVersion is %d, want 11", params.Name, params.HardForkGates.MLDSA44SignaturesBlockVersion)
		}
		highestVersion := constants.BlockVersionForDAAScore(params.POWScores, math.MaxUint64)
		if params.MLDSA44SignaturesActive(highestVersion) {
			t.Fatalf("%s: ML-DSA-44 is active at the highest reachable block version %d", params.Name, highestVersion)
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
