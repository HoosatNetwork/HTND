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

// TestCollisionOnDifferentCoinDoesNotLoseIt covers both directions of the (outpoint, BlockDAAScore)
// collision that put six coins into a mainnet node's UTXO set that its own acceptance history says
// were never created, and left out one 629,814 HTN output that it says was. containsWithDAAScore
// matches on outpoint and DAA score alone, so two entries can satisfy it and still be different
// coins; cancelling them against each other silently loses one, permanently, once the diff reaches
// the materialised UTXO set.
func TestCollisionOnDifferentCoinDoesNotLoseIt(t *testing.T) {
	_, _, _, outpoint0, _, _, _, _, _, _ := testFixtures()
	const daaScore = 7
	scriptA := &externalapi.ScriptPublicKey{Script: []byte{0xaa}, Version: 0}
	scriptB := &externalapi.ScriptPublicKey{Script: []byte{0xbb}, Version: 0}

	t.Run("addEntry must not cancel an addition against a different coin's pending removal", func(t *testing.T) {
		diff := newMutableUTXODiff()
		pendingRemoval := NewUTXOEntry(500, scriptA, false, daaScore)
		diff.toRemove.add(outpoint0, pendingRemoval)

		// Non-coinbase on purpose: the 629,814 HTN output actually lost on mainnet was not a coinbase,
		// and the pre-existing handling of this collision only covered coinbases.
		created := NewUTXOEntry(1000, scriptB, false, daaScore)
		if err := diff.addEntry(outpoint0, created); err != nil {
			t.Fatalf("addEntry: %+v", err)
		}

		got, ok := diff.toAdd.Get(outpoint0)
		if !ok {
			t.Fatalf("the created coin was dropped from toAdd: it collided with a pending removal of a "+
				"DIFFERENT coin (amount %d vs %d) and was silently cancelled out",
				pendingRemoval.Amount(), created.Amount())
		}
		if got.Amount() != created.Amount() || !got.ScriptPublicKey().Equal(created.ScriptPublicKey()) {
			t.Fatalf("toAdd holds the wrong coin: got amount %d, want %d", got.Amount(), created.Amount())
		}
	})

	t.Run("removeEntry must not drop a spend because a different coin sits in toAdd", func(t *testing.T) {
		diff := newMutableUTXODiff()
		unrelatedAddition := NewUTXOEntry(1000, scriptB, true, daaScore)
		diff.toAdd.add(outpoint0, unrelatedAddition)

		spent := NewUTXOEntry(500, scriptA, false, daaScore)
		if err := diff.removeEntry(outpoint0, spent); err != nil {
			t.Fatalf("removeEntry: %+v", err)
		}

		got, ok := diff.toRemove.Get(outpoint0)
		if !ok {
			t.Fatalf("the spend was not recorded in toRemove: removeEntry matched a DIFFERENT coin in "+
				"toAdd (amount %d vs %d) and dropped the removal, leaving the spent coin alive in the "+
				"resulting UTXO set", unrelatedAddition.Amount(), spent.Amount())
		}
		if got.Amount() != spent.Amount() {
			t.Fatalf("toRemove holds the wrong coin: got amount %d, want %d", got.Amount(), spent.Amount())
		}
	})

	t.Run("a genuine same-coin cancellation still cancels", func(t *testing.T) {
		diff := newMutableUTXODiff()
		coin := NewUTXOEntry(500, scriptA, false, daaScore)
		diff.toRemove.add(outpoint0, coin)
		if err := diff.addEntry(outpoint0, NewUTXOEntry(500, scriptA, false, daaScore)); err != nil {
			t.Fatalf("addEntry: %+v", err)
		}
		if diff.toRemove.Contains(outpoint0) || diff.toAdd.Contains(outpoint0) {
			t.Fatalf("re-adding the identical coin should cancel the pending removal, leaving the outpoint "+
				"in neither collection; toAdd=%s toRemove=%s", diff.toAdd, diff.toRemove)
		}
	})
}
