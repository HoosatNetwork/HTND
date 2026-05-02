package transactionvalidator

import (
	"testing"

	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/constants"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/utxo"
)

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
