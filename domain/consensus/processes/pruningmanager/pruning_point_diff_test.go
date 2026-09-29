package pruningmanager

import (
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/subnetworks"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/utxo"
)

var diffTestScript = &externalapi.ScriptPublicKey{Script: []byte{0x51}, Version: 0}

func diffTestCoinbase(value uint64) *externalapi.DomainTransaction {
	return &externalapi.DomainTransaction{
		SubnetworkID: subnetworks.SubnetworkIDCoinbase,
		Outputs:      []*externalapi.DomainTransactionOutput{{Value: value, ScriptPublicKey: diffTestScript}},
	}
}

// diffTestSpend spends outpoint, whose entry in the spending block's view is entry, into one output of value.
func diffTestSpend(outpoint externalapi.DomainOutpoint, entry externalapi.UTXOEntry, value uint64) *externalapi.DomainTransaction {
	return &externalapi.DomainTransaction{
		SubnetworkID: subnetworks.SubnetworkIDNative,
		Inputs:       []*externalapi.DomainTransactionInput{{PreviousOutpoint: outpoint, UTXOEntry: entry}},
		Outputs:      []*externalapi.DomainTransactionOutput{{Value: value, ScriptPublicKey: diffTestScript}},
	}
}

func diffTestOutpoint(transaction *externalapi.DomainTransaction) externalapi.DomainOutpoint {
	return externalapi.DomainOutpoint{TransactionID: *consensushashing.TransactionID(transaction), Index: 0}
}

type diffTestChainBlock struct {
	daaScore     uint64
	transactions []*externalapi.DomainTransaction
}

// applyToSet applies the chain blocks to set with set semantics: a spend deletes the coin, and an output replaces
// whatever the set held at that outpoint. This is the set the current pruning point's commitment describes.
func applyToSet(set map[externalapi.DomainOutpoint]externalapi.UTXOEntry, blocks []diffTestChainBlock) {
	for _, block := range blocks {
		for _, transaction := range block.transactions {
			for _, input := range transaction.Inputs {
				delete(set, input.PreviousOutpoint)
			}
			isCoinbase := transaction.SubnetworkID.Equal(&subnetworks.SubnetworkIDCoinbase)
			for i, output := range transaction.Outputs {
				outpoint := externalapi.DomainOutpoint{TransactionID: *consensushashing.TransactionID(transaction), Index: uint32(i)}
				set[outpoint] = utxo.NewUTXOEntry(output.Value, output.ScriptPublicKey, isCoinbase, block.daaScore)
			}
		}
	}
}

// exactSetDiff is the diff from previous to current, entry by entry. It is what the diff-chain walk produces.
func exactSetDiff(previous, current map[externalapi.DomainOutpoint]externalapi.UTXOEntry) (
	toAdd, toRemove map[externalapi.DomainOutpoint]externalapi.UTXOEntry,
) {
	toAdd = map[externalapi.DomainOutpoint]externalapi.UTXOEntry{}
	toRemove = map[externalapi.DomainOutpoint]externalapi.UTXOEntry{}
	for outpoint, entry := range previous {
		if other, ok := current[outpoint]; !ok || !other.Equal(entry) {
			toRemove[outpoint] = entry
		}
	}
	for outpoint, entry := range current {
		if other, ok := previous[outpoint]; !ok || !other.Equal(entry) {
			toAdd[outpoint] = entry
		}
	}
	return toAdd, toRemove
}

func requireSameEntries(t *testing.T, name string, got externalapi.UTXOCollection,
	want map[externalapi.DomainOutpoint]externalapi.UTXOEntry,
) {
	t.Helper()
	if got.Len() != len(want) {
		t.Errorf("%s: got %d entries, want %d", name, got.Len(), len(want))
	}
	for outpoint, entry := range want {
		gotEntry, ok := got.Get(&outpoint)
		if !ok {
			t.Errorf("%s: missing %s (%s)", name, &outpoint, describeEntry(entry))
			continue
		}
		if !gotEntry.Equal(entry) {
			t.Errorf("%s: %s is %s, want %s", name, &outpoint, describeEntry(gotEntry), describeEntry(entry))
		}
	}
}

// TestAcceptanceReplayMatchesTheExactPruningPointDiff pins that the acceptance-data derivation of the pruning point
// diff equals the exact difference between the previous and current pruning point UTXO sets, which is what the
// diff-chain walk derives. The cases that need reconciling are the ones where a chain block after the previous
// pruning point creates a coin the previous set already holds: a byte-identical coinbase accepted again.
func TestAcceptanceReplayMatchesTheExactPruningPointDiff(t *testing.T) {
	const previousDAAScore = 100

	heldCoinbase := diffTestCoinbase(50)       // in the previous set, restamped, still held at the end
	spentAfterRestamp := diffTestCoinbase(60)  // in the previous set, restamped, then spent
	spentThenRecreated := diffTestCoinbase(70) // in the previous set, spent, then accepted again
	untouched := diffTestCoinbase(80)          // in the previous set, never mentioned
	spentFromPrevious := diffTestCoinbase(90)  // in the previous set, spent
	createdInRange := diffTestCoinbase(110)    // new, still held
	createdAndSpentInRange := diffTestCoinbase(120)

	previousEntry := func(transaction *externalapi.DomainTransaction) externalapi.UTXOEntry {
		return utxo.NewUTXOEntry(transaction.Outputs[0].Value, diffTestScript, true, previousDAAScore)
	}
	previous := map[externalapi.DomainOutpoint]externalapi.UTXOEntry{}
	for _, transaction := range []*externalapi.DomainTransaction{
		heldCoinbase, spentAfterRestamp, spentThenRecreated, untouched, spentFromPrevious,
	} {
		previous[diffTestOutpoint(transaction)] = previousEntry(transaction)
	}

	restampedEntry := func(transaction *externalapi.DomainTransaction, daaScore uint64) externalapi.UTXOEntry {
		return utxo.NewUTXOEntry(transaction.Outputs[0].Value, diffTestScript, true, daaScore)
	}
	blocks := []diffTestChainBlock{
		{daaScore: 200, transactions: []*externalapi.DomainTransaction{
			heldCoinbase, spentAfterRestamp, createdInRange, createdAndSpentInRange,
			diffTestSpend(diffTestOutpoint(spentThenRecreated), previousEntry(spentThenRecreated), 1),
			diffTestSpend(diffTestOutpoint(spentFromPrevious), previousEntry(spentFromPrevious), 2),
		}},
		{daaScore: 300, transactions: []*externalapi.DomainTransaction{
			spentThenRecreated,
			diffTestSpend(diffTestOutpoint(spentAfterRestamp), restampedEntry(spentAfterRestamp, 200), 3),
			diffTestSpend(diffTestOutpoint(createdAndSpentInRange), restampedEntry(createdAndSpentInRange, 200), 4),
		}},
	}

	current := make(map[externalapi.DomainOutpoint]externalapi.UTXOEntry, len(previous))
	for outpoint, entry := range previous {
		current[outpoint] = entry
	}
	applyToSet(current, blocks)
	wantToAdd, wantToRemove := exactSetDiff(previous, current)

	// The diff-chain walk ends with DiffFrom between the two sets expressed as diffs from a common base. With an empty
	// base it must agree with the exact difference, or the oracle is not the chain walk's answer.
	fromBase := func(set map[externalapi.DomainOutpoint]externalapi.UTXOEntry) externalapi.UTXODiff {
		diff, err := utxo.NewUTXODiffFromCollections(utxo.NewUTXOCollection(set),
			utxo.NewUTXOCollection(map[externalapi.DomainOutpoint]externalapi.UTXOEntry{}))
		if err != nil {
			t.Fatalf("NewUTXODiffFromCollections: %+v", err)
		}
		return diff
	}
	chainWalk, err := fromBase(previous).DiffFrom(fromBase(current))
	if err != nil {
		t.Fatalf("DiffFrom: %+v", err)
	}
	requireSameEntries(t, "chain-walk toAdd", chainWalk.ToAdd(), wantToAdd)
	requireSameEntries(t, "chain-walk toRemove", chainWalk.ToRemove(), wantToRemove)

	replay := utxo.NewMutableUTXODiff()
	created := map[externalapi.DomainOutpoint]struct{}{}
	for _, block := range blocks {
		transactionAcceptanceData := make([]*externalapi.TransactionAcceptanceData, len(block.transactions))
		for i, transaction := range block.transactions {
			transactionAcceptanceData[i] = &externalapi.TransactionAcceptanceData{Transaction: transaction, IsAccepted: true}
		}
		acceptanceData := externalapi.AcceptanceData{{
			BlockHash:                 externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{byte(block.daaScore)}),
			TransactionAcceptanceData: transactionAcceptanceData,
		}}
		if err := utxo.ApplyAcceptanceDataToDiff(replay, acceptanceData, block.daaScore); err != nil {
			t.Fatalf("ApplyAcceptanceDataToDiff: %+v", err)
		}
		if err := addAcceptedOutpoints(created, acceptanceData); err != nil {
			t.Fatalf("addAcceptedOutpoints: %+v", err)
		}
	}

	lookups := 0
	reconciled, err := reconcileReplayWithPreviousSet(replay.ToImmutable(), created,
		func(outpoint *externalapi.DomainOutpoint) (externalapi.UTXOEntry, bool, error) {
			lookups++
			entry, ok := previous[*outpoint]
			return entry, ok, nil
		})
	if err != nil {
		t.Fatalf("reconcileReplayWithPreviousSet: %+v", err)
	}

	requireSameEntries(t, "toAdd", reconciled.ToAdd(), wantToAdd)
	requireSameEntries(t, "toRemove", reconciled.ToRemove(), wantToRemove)
	if lookups != len(created) {
		t.Errorf("looked up %d outpoints in the previous set, want only the %d the replay created", lookups, len(created))
	}

	// The unreconciled replay is what the acceptance-data derivation returned before: it misses both removals.
	heldOutpoint, spentAfterRestampOutpoint := diffTestOutpoint(heldCoinbase), diffTestOutpoint(spentAfterRestamp)
	if replay.ToRemove().Contains(&heldOutpoint) || replay.ToRemove().Contains(&spentAfterRestampOutpoint) {
		t.Fatalf("the test no longer exercises the restamp cases: the raw replay already removes them")
	}
}
