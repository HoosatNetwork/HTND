package utxo

import (
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
)

func op(b byte) externalapi.DomainOutpoint {
	return externalapi.DomainOutpoint{
		TransactionID: *externalapi.NewDomainTransactionIDFromByteArray(
			&[externalapi.DomainHashSize]byte{b}),
		Index: 0,
	}
}

func entryAt(daaScore uint64) externalapi.UTXOEntry {
	return NewUTXOEntry(1000, &externalapi.ScriptPublicKey{Script: []byte{0x51}, Version: 0},
		false, daaScore)
}

// TestIntersectionRemainderKeepsPreExistingEntries pins that
// intersectionWithRemainderHavingDAAScoreInPlace appends to remainder rather than destroying what
// is already there.
//
// The function has two paths, chosen by the relative SIZE of the two collections, and they
// disagreed. The standard path only ever assigns into remainder. The fast path copied all of
// collection1 into remainder and then DELETED the matched outpoints - taking any pre-existing
// remainder entry for that outpoint with them.
//
// At the withDiffInPlace call site, remainder is this.toRemove, which is not empty. So composing
// two diffs could silently drop a pending removal - and which path ran depended on collection
// sizes, so two nodes composing the same diffs could reach different UTXO sets. A dropped removal
// leaves the old coin in place with its old BlockDAAScore, which is exactly the wrong-stamp
// signature found on mainnet: 23 coins whose stamp contradicted the node's own acceptance data,
// every one of them too high, never too low.
func TestIntersectionRemainderKeepsPreExistingEntries(t *testing.T) {
	a, b := op(1), op(2)

	run := func(collection1, collection2 utxoCollection) (utxoCollection, utxoCollection) {
		result := make(utxoCollection)
		// A removal already pending for a, at a DIFFERENT DAA score - a restamp in progress.
		remainder := utxoCollection{a: entryAt(50)}
		intersectionWithRemainderHavingDAAScoreInPlace(collection1, collection2, result, remainder)
		return result, remainder
	}

	// Sizes that take the standard path (collection2 is not smaller).
	stdResult, stdRemainder := run(
		utxoCollection{a: entryAt(100)},
		utxoCollection{a: entryAt(100)},
	)
	if _, ok := stdResult[a]; !ok {
		t.Fatal("standard path: expected the matching outpoint in result")
	}
	if _, ok := stdRemainder[a]; !ok {
		t.Fatal("standard path: the pre-existing remainder entry must survive")
	}

	// Sizes that take the fast path (collection2 smaller than collection1). Same logical question
	// about outpoint a; b is only there to make collection1 larger.
	fastResult, fastRemainder := run(
		utxoCollection{a: entryAt(100), b: entryAt(100)},
		utxoCollection{a: entryAt(100)},
	)
	if _, ok := fastResult[a]; !ok {
		t.Fatal("fast path: expected the matching outpoint in result")
	}
	if _, ok := fastRemainder[a]; !ok {
		t.Error("fast path DESTROYED a pre-existing remainder entry that the standard path keeps - " +
			"the two paths must agree, or the result depends on collection sizes")
	}
	if entry, ok := fastRemainder[a]; ok && entry.BlockDAAScore() != 50 {
		t.Errorf("fast path overwrote the pre-existing remainder entry: daaScore %d, want 50",
			entry.BlockDAAScore())
	}
	if _, ok := fastRemainder[b]; !ok {
		t.Error("fast path: a non-matching collection1 entry belongs in remainder")
	}
}
