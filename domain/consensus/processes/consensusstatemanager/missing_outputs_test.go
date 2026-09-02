package consensusstatemanager

import (
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/subnetworks"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/utxo"
)

const testDAAScore = 221319694

// nonCoinbaseTransaction builds a minimal spendable-shaped transaction on the native subnetwork,
// so transactionhelper.IsCoinBase is false and the P2b path under test is the one that applies.
func nonCoinbaseTransaction(outputValue uint64) *externalapi.DomainTransaction {
	return &externalapi.DomainTransaction{
		Version: 0,
		Inputs:  []*externalapi.DomainTransactionInput{},
		Outputs: []*externalapi.DomainTransactionOutput{
			{
				Value:           outputValue,
				ScriptPublicKey: &externalapi.ScriptPublicKey{Script: []byte{0x51}, Version: 0},
			},
		},
		SubnetworkID: subnetworks.SubnetworkIDNative,
	}
}

// TestMissingOutputsAfterAddTransactionDetectsSilentDrop reproduces the exact shape that loses a
// real output with no error anywhere: AddTransaction's addEntry finds the outpoint already staged
// in toRemove with a matching BlockDAAScore, cancels the removal, and never places the output in
// toAdd. The transaction is non-coinbase, which is why the pre-existing check - gated on
// isCoinbase - could not see it.
func TestMissingOutputsAfterAddTransactionDetectsSilentDrop(t *testing.T) {
	transaction := nonCoinbaseTransaction(564928697363)
	transactionID := consensushashing.TransactionID(transaction)
	outpoint := externalapi.NewDomainOutpoint(transactionID, 0)

	// Stage the collision: the same outpoint already sits in toRemove at the same DAA score.
	staleEntry := utxo.NewUTXOEntry(
		transaction.Outputs[0].Value, transaction.Outputs[0].ScriptPublicKey, false, testDAAScore)
	base, err := utxo.NewUTXODiffFromCollections(
		utxo.NewUTXOCollection(map[externalapi.DomainOutpoint]externalapi.UTXOEntry{}),
		utxo.NewUTXOCollection(map[externalapi.DomainOutpoint]externalapi.UTXOEntry{*outpoint: staleEntry}),
	)
	if err != nil {
		t.Fatalf("could not build the base diff: %s", err)
	}
	diff := base.CloneMutable()

	if err := diff.AddTransaction(transaction, testDAAScore); err != nil {
		t.Fatalf("AddTransaction returned an error, but the bug under test is that it does not: %s", err)
	}

	// Precondition for the test to be meaningful: AddTransaction reported success and still did not
	// record the output.
	if _, ok := diff.ToAdd().Get(outpoint); ok {
		t.Fatalf("the output landed in toAdd, so this fixture no longer reproduces the silent drop")
	}

	missing := missingOutputsAfterAddTransaction(diff, transaction, transactionID)
	if len(missing) != 1 || missing[0] != 0 {
		t.Fatalf("missingOutputsAfterAddTransaction = %v, want [0]", missing)
	}
}

// The detector must stay quiet on the ordinary path, or the warn it drives is noise.
func TestMissingOutputsAfterAddTransactionQuietOnSuccess(t *testing.T) {
	transaction := nonCoinbaseTransaction(14264714524)
	transactionID := consensushashing.TransactionID(transaction)

	diff := utxo.NewMutableUTXODiff()
	if err := diff.AddTransaction(transaction, testDAAScore); err != nil {
		t.Fatalf("AddTransaction failed on a clean diff: %s", err)
	}

	if missing := missingOutputsAfterAddTransaction(diff, transaction, transactionID); len(missing) != 0 {
		t.Fatalf("missingOutputsAfterAddTransaction = %v on a clean add, want none", missing)
	}
}

func TestMissingOutputsAfterAddTransactionHandlesNils(t *testing.T) {
	transaction := nonCoinbaseTransaction(1)
	transactionID := consensushashing.TransactionID(transaction)
	diff := utxo.NewMutableUTXODiff()

	if missing := missingOutputsAfterAddTransaction(nil, transaction, transactionID); missing != nil {
		t.Errorf("nil diff: got %v, want nil", missing)
	}
	if missing := missingOutputsAfterAddTransaction(diff, nil, transactionID); missing != nil {
		t.Errorf("nil transaction: got %v, want nil", missing)
	}
	if missing := missingOutputsAfterAddTransaction(diff, transaction, nil); missing != nil {
		t.Errorf("nil transaction ID: got %v, want nil", missing)
	}
}
