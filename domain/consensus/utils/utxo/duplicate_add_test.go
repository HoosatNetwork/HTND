package utxo

import (
	"strings"
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
)

func outpointOf(b byte, index uint32) *externalapi.DomainOutpoint {
	return &externalapi.DomainOutpoint{
		TransactionID: *externalapi.NewDomainTransactionIDFromByteArray(
			&[externalapi.DomainHashSize]byte{b}),
		Index: index,
	}
}

// TestAddEntryToleratesDuplicateOfTheSameCoin pins the rule that unstuck a deadlocked IBD: a set
// holds a coin once, so adding an outpoint already pending in toAdd with the same value is a no-op.
// It used to error for anything but a coinbase, which aborted the whole virtual update - and since
// the block is then re-requested and fails identically, the node never advanced again.
func TestAddEntryToleratesDuplicateOfTheSameCoin(t *testing.T) {
	script := &externalapi.ScriptPublicKey{Script: []byte{0x51}, Version: 0}

	for _, isCoinbase := range []bool{false, true} {
		diff := NewMutableUTXODiff().(*mutableUTXODiff)
		outpoint := outpointOf(1, 0)

		if err := diff.addEntry(outpoint, NewUTXOEntry(500, script, isCoinbase, 100)); err != nil {
			t.Fatalf("isCoinbase=%t: first add: %v", isCoinbase, err)
		}
		// Adding the identical entry again is a no-op: a set holds a coin once.
		if err := diff.addEntry(outpoint, NewUTXOEntry(500, script, isCoinbase, 100)); err != nil {
			t.Fatalf("isCoinbase=%t: re-adding the identical entry should be a no-op, got: %v",
				isCoinbase, err)
		}
		if diff.toAdd.Len() != 1 {
			t.Fatalf("isCoinbase=%t: expected the set to hold the coin once, got %d entries",
				isCoinbase, diff.toAdd.Len())
		}

		// Same coin at a DIFFERENT DAA score is a restamp, and the INCOMING score wins.
		//
		// This assertion was the other way round when the tolerance was first widened, on the
		// reasoning that a set holds a coin once so the first record of it should stand. That was
		// wrong, and a live node showed why within hours: a coin's stamp is the DAA score of the
		// block that merged it, and a block's own acceptance data is authoritative for its own past.
		// Keeping the stale score left the diff describing the coin differently from the acceptance
		// data beside it, blockOnlyCarriesTheInheritedOffset refused to tolerate a block whose two
		// records of itself disagreed, and the node wedged on it - "output 0 is in the block's past
		// UTXO set with different contents than its acceptance data describes", on every retry.
		if err := diff.addEntry(outpoint, NewUTXOEntry(500, script, isCoinbase, 200)); err != nil {
			t.Fatalf("isCoinbase=%t: a restamp should be applied, not refused, got: %v",
				isCoinbase, err)
		}
		if diff.toAdd.Len() != 1 {
			t.Fatalf("isCoinbase=%t: a restamp must replace, not add a second entry; got %d",
				isCoinbase, diff.toAdd.Len())
		}
		entry, ok := diff.toAdd.Get(outpoint)
		if !ok {
			t.Fatalf("isCoinbase=%t: coin vanished from toAdd", isCoinbase)
		}
		if entry.BlockDAAScore() != 200 {
			t.Fatalf("isCoinbase=%t: expected the incoming score to win (daaScore 200), got %d",
				isCoinbase, entry.BlockDAAScore())
		}
	}
}

// TestAddEntryStillRefusesDifferentCoinsAtOneOutpoint is the other half: tolerating a same-valued
// duplicate must not become tolerating a collision between two genuinely different coins, which
// would let one of them be dropped from the set.
func TestAddEntryStillRefusesDifferentCoinsAtOneOutpoint(t *testing.T) {
	script := &externalapi.ScriptPublicKey{Script: []byte{0x51}, Version: 0}
	diff := NewMutableUTXODiff().(*mutableUTXODiff)
	outpoint := outpointOf(2, 0)

	if err := diff.addEntry(outpoint, NewUTXOEntry(500, script, false, 100)); err != nil {
		t.Fatalf("first add: %v", err)
	}
	err := diff.addEntry(outpoint, NewUTXOEntry(999, script, false, 100))
	if err == nil {
		t.Fatal("expected an error when a different-valued coin collides at the same outpoint")
	}
	// The message has to carry both coins, or there is nothing to diagnose the collision from.
	for _, want := range []string{"amount=500", "amount=999"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("expected the error to report %s, got: %v", want, err)
		}
	}
}
