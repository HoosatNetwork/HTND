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
		// Same coin, different DAA score - which is what a different merging block produces.
		if err := diff.addEntry(outpoint, NewUTXOEntry(500, script, isCoinbase, 200)); err != nil {
			t.Fatalf("isCoinbase=%t: duplicate add of the same coin should be a no-op, got: %v",
				isCoinbase, err)
		}
		if diff.toAdd.Len() != 1 {
			t.Fatalf("isCoinbase=%t: expected the set to hold the coin once, got %d entries",
				isCoinbase, diff.toAdd.Len())
		}
		// First add wins, matching ApplyAcceptanceDataToMultiset's skipDuplicate tie-break.
		entry, ok := diff.toAdd.Get(outpoint)
		if !ok {
			t.Fatalf("isCoinbase=%t: coin vanished from toAdd", isCoinbase)
		}
		if entry.BlockDAAScore() != 100 {
			t.Fatalf("isCoinbase=%t: expected the first add to win (daaScore 100), got %d",
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
