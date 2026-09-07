package consensusstatemanager

import (
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/utxo"
)

// TestBlockOnlyCarriesTheInheritedOffset is what lets a node keep working on a baseline it knows is
// wrong without going blind.
//
// The node cannot reproduce any block's UTXO commitment while its starting set is incomplete: MuHash
// is homomorphic, so the offset reaches every descendant, and the hash of a wrong set is wrong
// however correct the arithmetic on top of it. Tolerating that is the only way to sync at all. But
// tolerating it as "ignore every failure on this block" also waves through a block whose own
// resolution is broken, so new corruption lands on top of the old with the same log line.
//
// A block's acceptance data and its UTXO diff are two records of the same thing, and comparing them
// needs no knowledge of the true set. Agreement means the block's arithmetic is right and only its
// inherited starting point is wrong. Disagreement means the block created or destroyed something its
// own record does not account for, which no baseline offset can explain.
func TestBlockOnlyCarriesTheInheritedOffset(t *testing.T) {
	const daaScore = 900
	script := &externalapi.ScriptPublicKey{Script: []byte{0x51}, Version: 0}
	coinbase := acceptedCoinbase(100)
	coinbaseID := consensushashing.TransactionID(coinbase)
	created := externalapi.DomainOutpoint{TransactionID: *coinbaseID, Index: 0}
	correctEntry := utxo.NewUTXOEntry(100, script, true, daaScore)

	diffWith := func(toAdd map[externalapi.DomainOutpoint]externalapi.UTXOEntry,
		toRemove map[externalapi.DomainOutpoint]externalapi.UTXOEntry,
	) externalapi.UTXODiff {
		t.Helper()
		if toRemove == nil {
			toRemove = map[externalapi.DomainOutpoint]externalapi.UTXOEntry{}
		}
		diff, err := utxo.NewUTXODiffFromCollections(utxo.NewUTXOCollection(toAdd),
			utxo.NewUTXOCollection(toRemove))
		if err != nil {
			t.Fatalf("NewUTXODiffFromCollections: %+v", err)
		}
		return diff
	}

	tests := []struct {
		name      string
		diff      externalapi.UTXODiff
		tolerable bool
	}{{
		name:      "diff matches acceptance - the block is correct and only its baseline is not",
		diff:      diffWith(map[externalapi.DomainOutpoint]externalapi.UTXOEntry{created: correctEntry}, nil),
		tolerable: true,
	}, {
		// Created and destroyed inside this same past: absent from toAdd for a reason that nets to
		// nothing, and the multiset nets to nothing too.
		name: "coin created and spent in the same past",
		diff: diffWith(map[externalapi.DomainOutpoint]externalapi.UTXOEntry{},
			map[externalapi.DomainOutpoint]externalapi.UTXOEntry{created: correctEntry}),
		tolerable: true,
	}, {
		// The block says it created a coin and its own past does not hold it. With no virtual store
		// wired in, a coin absent from the diff falls through to the (unavailable) virtual lookup and
		// reads as absent - which is the verdict for a coin genuinely nowhere in the block's past.
		name:      "accepted output absent from the block's past",
		diff:      diffWith(map[externalapi.DomainOutpoint]externalapi.UTXOEntry{}, nil),
		tolerable: false,
	}, {
		// Present, but not the coin the acceptance data describes - a different amount is a different
		// coin, and the commitment would be wrong for a reason the baseline does not explain.
		name: "accepted output present with different contents",
		diff: diffWith(map[externalapi.DomainOutpoint]externalapi.UTXOEntry{
			created: utxo.NewUTXOEntry(99, script, true, daaScore),
		}, nil),
		tolerable: false,
	}, {
		// The DAA stamp is part of the committed preimage, so a wrong one is a wrong coin.
		name: "accepted output stamped with the wrong DAA score",
		diff: diffWith(map[externalapi.DomainOutpoint]externalapi.UTXOEntry{
			created: utxo.NewUTXOEntry(100, script, true, daaScore-1),
		}, nil),
		tolerable: false,
	}}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// No coin in virtual: the block's past view is exactly what its diff says.
			got, reason := blockOnlyCarriesTheInheritedOffset(acceptanceDataOf(coinbase), test.diff,
				daaScore, nil)
			if got != test.tolerable {
				t.Errorf("expected carriesOffsetOnly=%t, got %t (reason: %q)", test.tolerable, got, reason)
			}
			if !got && reason == "" {
				t.Error("a refusal must say what the block did that the baseline cannot explain")
			}
		})
	}
}

// With no diff to compare against there is nothing to convict the block on, and disqualifying it on
// missing information would be worse than tolerating it.
func TestBlockWithNoDiffIsTreatedAsCarryingTheOffset(t *testing.T) {
	carries, _ := blockOnlyCarriesTheInheritedOffset(acceptanceDataOf(acceptedCoinbase(100)), nil, 900, nil)
	if !carries {
		t.Error("with no diff available the block must not be convicted of anything")
	}
}

// TestCoinAlreadyInVirtualIsNotTreatedAsMissing is the flaw that testing diff membership alone
// produced, caught by the end-to-end survey test before it could ship.
//
// pastUTXODiff is a diff from VIRTUAL to the block's past, so a coin that both virtual and the block
// hold needs no diff entry at all. Reading its absence from toAdd as "the block never created it"
// makes the verdict depend on where virtual happens to sit, and convicts a perfectly correct block
// the moment virtual advances past it - which is every block, once a node is synced.
func TestCoinAlreadyInVirtualIsNotTreatedAsMissing(t *testing.T) {
	const daaScore = 900
	script := &externalapi.ScriptPublicKey{Script: []byte{0x51}, Version: 0}
	coinbase := acceptedCoinbase(100)
	created := &externalapi.DomainOutpoint{
		TransactionID: *consensushashing.TransactionID(coinbase),
		Index:         0,
	}

	// The block's diff says nothing about the coin, because virtual holds it too.
	emptyDiff, err := utxo.NewUTXODiffFromCollections(
		utxo.NewUTXOCollection(map[externalapi.DomainOutpoint]externalapi.UTXOEntry{}),
		utxo.NewUTXOCollection(map[externalapi.DomainOutpoint]externalapi.UTXOEntry{}))
	if err != nil {
		t.Fatalf("NewUTXODiffFromCollections: %+v", err)
	}
	virtualHolds := func(outpoint *externalapi.DomainOutpoint) (externalapi.UTXOEntry, bool) {
		if outpoint.Equal(created) {
			return utxo.NewUTXOEntry(100, script, true, daaScore), true
		}
		return nil, false
	}

	carries, reason := blockOnlyCarriesTheInheritedOffset(acceptanceDataOf(coinbase), emptyDiff,
		daaScore, virtualHolds)
	if !carries {
		t.Errorf("a coin virtual already holds is in the block's past view and must not be reported "+
			"missing: %s", reason)
	}

	// And it is still convicted when virtual holds a different coin at that outpoint, because that is
	// a real disagreement rather than an artefact of where virtual is.
	virtualHoldsWrong := func(outpoint *externalapi.DomainOutpoint) (externalapi.UTXOEntry, bool) {
		if outpoint.Equal(created) {
			return utxo.NewUTXOEntry(99, script, true, daaScore), true
		}
		return nil, false
	}
	if carries, _ := blockOnlyCarriesTheInheritedOffset(acceptanceDataOf(coinbase), emptyDiff,
		daaScore, virtualHoldsWrong); carries {
		t.Error("a coin present with different contents than the acceptance data describes is a real " +
			"disagreement and must not be excused as an inherited offset")
	}
}
