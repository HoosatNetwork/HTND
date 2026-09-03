package utxo

import (
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/consensushashing"
)

// TestAddTransactionAtomicity ensures that when AddTransaction fails partway through - here on a
// transaction's second output colliding with a pre-existing, differently-DAA-scored toAdd entry -
// it leaves the diff exactly as it found it, rather than persisting the already-processed input
// removal and first output addition under a call that the caller will treat as failed/not-accepted.
func TestAddTransactionAtomicity(t *testing.T) {
	_, _, _, outpoint0, _, _, utxoEntry0, _, _, _ := testFixtures()

	transaction := &externalapi.DomainTransaction{
		Version: 0,
		Inputs: []*externalapi.DomainTransactionInput{
			{
				PreviousOutpoint: *outpoint0,
				UTXOEntry:        utxoEntry0,
			},
		},
		Outputs: []*externalapi.DomainTransactionOutput{
			{Value: 100, ScriptPublicKey: &externalapi.ScriptPublicKey{Script: []byte{0x01}, Version: 0}},
			{Value: 200, ScriptPublicKey: &externalapi.ScriptPublicKey{Script: []byte{0x02}, Version: 0}},
		},
		SubnetworkID: externalapi.DomainSubnetworkID{},
		Payload:      []byte{},
	}

	const blockDAAScore = 5
	txID := consensushashing.TransactionID(transaction)
	collidingOutpoint := externalapi.NewDomainOutpoint(txID, 1)
	// Different DAA score and not coinbase, so the second output's addEntry call hits the plain
	// "Cannot add outpoint twice" error rather than one of the tolerated-duplicate branches.
	conflictingEntry := NewUTXOEntry(999, &externalapi.ScriptPublicKey{Script: []byte{0xff}, Version: 0}, false, blockDAAScore+1)

	diff := newMutableUTXODiff()
	diff.toAdd.add(collidingOutpoint, conflictingEntry)
	before := diff.clone()

	err := diff.AddTransaction(transaction, blockDAAScore)
	if err == nil {
		t.Fatalf("expected AddTransaction to fail on the colliding second output, got nil error")
	}

	if !utxoCollectionsEqual(diff.toAdd, before.toAdd) {
		t.Fatalf("toAdd was not fully rolled back after a failed AddTransaction.\nbefore: %s\nafter:  %s",
			before.toAdd, diff.toAdd)
	}
	if !utxoCollectionsEqual(diff.toRemove, before.toRemove) {
		t.Fatalf("toRemove was not fully rolled back after a failed AddTransaction.\nbefore: %s\nafter:  %s",
			before.toRemove, diff.toRemove)
	}
}
