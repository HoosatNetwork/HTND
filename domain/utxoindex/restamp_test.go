package utxoindex

import (
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/utxo"
)

func testScript() *externalapi.ScriptPublicKey {
	return &externalapi.ScriptPublicKey{Script: []byte{0x51, 0x2f, 0x52}, Version: 0}
}

func testOutpoint() *externalapi.DomainOutpoint {
	return &externalapi.DomainOutpoint{
		TransactionID: *externalapi.NewDomainTransactionIDFromByteArray(
			&[externalapi.DomainHashSize]byte{7}),
		Index: 3,
	}
}

func stagingStore() *utxoIndexStore {
	return &utxoIndexStore{
		toAdd:    make(map[ScriptPublicKeyString]UTXOOutpointEntryPairs),
		toRemove: make(map[ScriptPublicKeyString]UTXOOutpointEntryPairs),
	}
}

// TestRestampIsNotCancelledAway is the regression test for wrong balances.
//
// A coin's BlockDAAScore is the DAA score of the block that merged it, and it changes when a
// different block takes over merging. The change reaches the index as a removal of the old entry
// followed by an addition of the new one, because UTXOIndex.Update always applies ToRemove before
// ToAdd.
//
// add() used to treat any staged removal of the same outpoint as a cancellation without comparing
// the entries: it deleted the removal and returned, discarding the new entry entirely. Nothing was
// then written, so the stale row stayed in the database. On mainnet that left 130,857 of 18,276,121
// index entries disagreeing with the node's own consensus UTXO set, every one of them stale-high -
// and the index is what GetUtxosByAddresses answers from.
func TestRestampIsNotCancelledAway(t *testing.T) {
	script, outpoint := testScript(), testOutpoint()
	key := ScriptPublicKeyString(script.String())

	oldEntry := utxo.NewUTXOEntry(66666666, script, true, 223730332)
	newEntry := utxo.NewUTXOEntry(66666666, script, true, 223730355)

	uis := stagingStore()
	if err := uis.remove(script, outpoint, oldEntry); err != nil {
		t.Fatalf("remove: %+v", err)
	}
	if err := uis.add(script, outpoint, newEntry); err != nil {
		t.Fatalf("add: %+v", err)
	}

	staged, ok := uis.toAdd[key][*outpoint]
	if !ok {
		t.Fatal("the restamped entry was discarded: nothing would be written, so the stale row survives")
	}
	if staged.BlockDAAScore() != newEntry.BlockDAAScore() {
		t.Errorf("staged the wrong entry: daaScore %d, want %d",
			staged.BlockDAAScore(), newEntry.BlockDAAScore())
	}
	// The removal stays staged too. commit() deletes before it puts, so the new value lands.
	if _, ok := uis.toRemove[key][*outpoint]; !ok {
		t.Error("the removal of the old entry should remain staged")
	}
}

// TestIdenticalPairStillCancels keeps the optimisation that was actually correct: a removal and an
// addition of the very same coin are a no-op and must not produce database traffic.
func TestIdenticalPairStillCancels(t *testing.T) {
	script, outpoint := testScript(), testOutpoint()
	key := ScriptPublicKeyString(script.String())
	entry := utxo.NewUTXOEntry(66666666, script, true, 223730332)

	uis := stagingStore()
	if err := uis.remove(script, outpoint, entry); err != nil {
		t.Fatalf("remove: %+v", err)
	}
	if err := uis.add(script, outpoint, entry); err != nil {
		t.Fatalf("add: %+v", err)
	}
	if _, ok := uis.toRemove[key][*outpoint]; ok {
		t.Error("an identical removal and addition must cancel")
	}
	if _, ok := uis.toAdd[key][*outpoint]; ok {
		t.Error("an identical removal and addition must cancel")
	}
}

// TestDifferingRemovalAfterAddIsKept is the mirror case in remove().
func TestDifferingRemovalAfterAddIsKept(t *testing.T) {
	script, outpoint := testScript(), testOutpoint()
	key := ScriptPublicKeyString(script.String())

	newEntry := utxo.NewUTXOEntry(66666666, script, true, 223730355)
	oldEntry := utxo.NewUTXOEntry(66666666, script, true, 223730332)

	uis := stagingStore()
	if err := uis.add(script, outpoint, newEntry); err != nil {
		t.Fatalf("add: %+v", err)
	}
	if err := uis.remove(script, outpoint, oldEntry); err != nil {
		t.Fatalf("remove: %+v", err)
	}
	if _, ok := uis.toRemove[key][*outpoint]; !ok {
		t.Error("a removal describing a different coin must not be cancelled away by a staged add")
	}
}
