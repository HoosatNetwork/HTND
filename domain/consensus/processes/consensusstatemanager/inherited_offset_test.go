package consensusstatemanager

import (
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/subnetworks"
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

// TestCoinCreatedAndSpentInTheSamePastLeavesNoDiffEntry is the case a live node hit within minutes
// of this check being deployed, and it was a false conviction.
//
// A coin that this same past both creates and spends leaves no trace in the diff at all.
// mutableUTXODiff.removeEntry cancels the pending toAdd rather than recording a removal, so the coin
// is in neither toAdd nor toRemove - and it was never in virtual either, having been created and
// destroyed before virtual ever saw it. The earlier version of this check only forgave the case
// where the coin sat in toRemove, so it convicted every block carrying a transaction that spends
// another transaction merged alongside it.
//
// A chain of compounding transactions is exactly that shape, which is why a node syncing live
// traffic reported a block as internally inconsistent when nothing was wrong with it.
func TestCoinCreatedAndSpentInTheSamePastLeavesNoDiffEntry(t *testing.T) {
	const daaScore = 900

	parent := acceptedCoinbase(100)
	parentID := consensushashing.TransactionID(parent)
	parentOutput := externalapi.DomainOutpoint{TransactionID: *parentID, Index: 0}

	// A second accepted transaction spending the first one's output, both merged by this block.
	child := acceptedTransactionOfKind(externalapi.DomainSubnetworkID{},
		[]*externalapi.DomainOutpoint{&parentOutput}, 90)
	childID := consensushashing.TransactionID(child)
	childOutput := externalapi.DomainOutpoint{TransactionID: *childID, Index: 0}

	acceptanceData := externalapi.AcceptanceData{{
		BlockHash: externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{3}),
		TransactionAcceptanceData: []*externalapi.TransactionAcceptanceData{
			{Transaction: parent, IsAccepted: true},
			{Transaction: child, IsAccepted: true},
		},
	}}

	// The child's output survives and is in toAdd. The parent's output was created and spent here,
	// so it appears nowhere - which is what the diff genuinely looks like.
	script := &externalapi.ScriptPublicKey{Script: []byte{0x51}, Version: 0}
	toAdd := map[externalapi.DomainOutpoint]externalapi.UTXOEntry{
		childOutput: utxo.NewUTXOEntry(90, script, false, daaScore),
	}
	diff, err := utxo.NewUTXODiffFromCollections(utxo.NewUTXOCollection(toAdd),
		utxo.NewUTXOCollection(map[externalapi.DomainOutpoint]externalapi.UTXOEntry{}))
	if err != nil {
		t.Fatalf("NewUTXODiffFromCollections: %+v", err)
	}

	// Virtual holds neither: the parent's output never reached it, and the child's is accounted for
	// by the diff.
	tolerable, reason := blockOnlyCarriesTheInheritedOffset(acceptanceData, diff, daaScore,
		func(*externalapi.DomainOutpoint) (externalapi.UTXOEntry, bool) { return nil, false })

	if !tolerable {
		t.Errorf("a coin created and spent in the same past nets to nothing and must not be treated "+
			"as a block losing a coin, got: %s", reason)
	}
}

// BenchmarkBlockOnlyCarriesTheInheritedOffset guards the cost of this scan, because the cost is the
// whole reason its shape is what it is.
//
// On a node with an offset baseline this runs for every block that resolves, and it used to run
// twice per block whether or not anything had failed. Two earlier versions of it allocated per call
// in ways that did not show up in any test: a spentInThisPast map built up front over every input in
// the merge set, and a fresh outpoint plus a fresh UTXO entry for every output compared. Together
// they were ~611 KB and ~6,000 allocations for the merge set below, all of it garbage by the time
// the function returned - which at a few hundred blocks a second during IBD is hundreds of megabytes
// a second of pure GC pressure.
//
// Run it with -benchmem. A jump in B/op here is the regression this is for.
func BenchmarkBlockOnlyCarriesTheInheritedOffset(b *testing.B) {
	const (
		mergedBlocks = 20
		txsPerBlock  = 50
		daaScore     = 900
	)
	script := &externalapi.ScriptPublicKey{Script: []byte{0x51}, Version: 0}
	toAdd := map[externalapi.DomainOutpoint]externalapi.UTXOEntry{}
	acceptanceData := make(externalapi.AcceptanceData, 0, mergedBlocks)

	var counter byte
	for blockIndex := 0; blockIndex < mergedBlocks; blockIndex++ {
		acceptances := make([]*externalapi.TransactionAcceptanceData, 0, txsPerBlock)
		for txIndex := 0; txIndex < txsPerBlock; txIndex++ {
			counter++
			kind := subnetworks.SubnetworkIDNative
			var inputs []*externalapi.DomainOutpoint
			if txIndex == 0 {
				kind = subnetworks.SubnetworkIDCoinbase
			} else {
				for i := 0; i < 2; i++ {
					spent := externalapi.NewDomainHashFromByteArray(
						&[externalapi.DomainHashSize]byte{counter, byte(blockIndex), byte(i)})
					inputs = append(inputs, &externalapi.DomainOutpoint{
						TransactionID: externalapi.DomainTransactionID(*spent),
						Index:         uint32(i),
					})
				}
			}
			transaction := acceptedTransactionOfKind(kind, inputs, 100, 200)
			// Distinct payloads so every transaction gets a distinct ID, as in a real merge set.
			transaction.Payload = []byte{counter, byte(blockIndex), byte(txIndex)}
			transactionID := consensushashing.TransactionID(transaction)
			for outputIndex, output := range transaction.Outputs {
				toAdd[externalapi.DomainOutpoint{TransactionID: *transactionID, Index: uint32(outputIndex)}] =
					utxo.NewUTXOEntry(output.Value, script, txIndex == 0, daaScore)
			}
			acceptances = append(acceptances, &externalapi.TransactionAcceptanceData{
				Transaction: transaction,
				IsAccepted:  true,
			})
		}
		acceptanceData = append(acceptanceData, &externalapi.BlockAcceptanceData{
			BlockHash: externalapi.NewDomainHashFromByteArray(
				&[externalapi.DomainHashSize]byte{byte(blockIndex)}),
			TransactionAcceptanceData: acceptances,
		})
	}

	diff, err := utxo.NewUTXODiffFromCollections(utxo.NewUTXOCollection(toAdd),
		utxo.NewUTXOCollection(map[externalapi.DomainOutpoint]externalapi.UTXOEntry{}))
	if err != nil {
		b.Fatalf("NewUTXODiffFromCollections: %+v", err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		carries, reason := blockOnlyCarriesTheInheritedOffset(acceptanceData, diff, daaScore, nil)
		if !carries {
			b.Fatalf("a merge set whose diff matches its acceptance data must be tolerable: %s", reason)
		}
	}
}
