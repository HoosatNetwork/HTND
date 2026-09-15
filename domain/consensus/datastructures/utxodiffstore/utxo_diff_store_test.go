package utxodiffstore

import (
	"testing"

	consensusdb "github.com/HoosatNetwork/HTND/domain/consensus/database"
	"github.com/HoosatNetwork/HTND/domain/consensus/datastructures/testutils"
	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/utxo"
)

func TestUTXODiffStoreRoundTripChildAndDelete(t *testing.T) {
	dbManager, prefixBucket, teardown := testutils.NewTestDB(t)
	defer teardown()

	store := New(prefixBucket, 10, false)

	blockHash := testutils.Hash(1)
	childHash := testutils.Hash(2)
	outpoint := testutils.Outpoint(1, 0)
	entry := testutils.UTXOEntry(100, 7)
	txToAdd := map[externalapi.DomainOutpoint]externalapi.UTXOEntry{*outpoint: entry}
	toAdd := utxo.NewUTXOCollection(txToAdd)
	toRemove := utxo.NewUTXOCollection(map[externalapi.DomainOutpoint]externalapi.UTXOEntry{})
	diff, err := utxo.NewUTXODiffFromCollections(toAdd, toRemove)
	if err != nil {
		t.Fatalf("NewUTXODiffFromCollections: %v", err)
	}

	stagingArea := model.NewStagingArea()
	store.Stage(stagingArea, blockHash, diff, childHash)
	if !store.IsStaged(stagingArea) {
		t.Fatalf("expected IsStaged to be true after Stage")
	}
	testutils.Commit(t, dbManager, stagingArea)

	stagingArea = model.NewStagingArea()
	gotDiff, err := store.UTXODiff(dbManager, stagingArea, blockHash)
	if err != nil {
		t.Fatalf("UTXODiff: %v", err)
	}
	if !gotDiff.Equal(diff) {
		t.Fatalf("unexpected diff")
	}

	hasChild, err := store.HasUTXODiffChild(dbManager, stagingArea, blockHash)
	if err != nil {
		t.Fatalf("HasUTXODiffChild: %v", err)
	}
	if !hasChild {
		t.Fatalf("expected HasUTXODiffChild to be true")
	}

	gotChild, err := store.UTXODiffChild(dbManager, stagingArea, blockHash)
	if err != nil {
		t.Fatalf("UTXODiffChild: %v", err)
	}
	if !gotChild.Equal(childHash) {
		t.Fatalf("unexpected child hash")
	}

	// Delete
	stagingArea = model.NewStagingArea()
	store.Delete(stagingArea, blockHash)
	if !store.IsStaged(stagingArea) {
		t.Fatalf("expected IsStaged to be true after Delete")
	}
	testutils.Commit(t, dbManager, stagingArea)

	stagingArea = model.NewStagingArea()
	_, err = store.UTXODiff(dbManager, stagingArea, blockHash)
	if err == nil || !consensusdb.IsNotFoundError(err) {
		t.Fatalf("expected not-found after delete, got %v", err)
	}
}

// TestUTXODiffStoreStagedNilChildHidesStoredChild pins that staging a block's diff with no diff child hides the child it
// has in the database from reads through the same staging area, as it does from HasUTXODiffChild and as committing it
// does. UTXODiffChild used to fall through to the stored child, so a walk from that block to virtual within the staging
// area followed a pointer the staging area had already removed, and could loop back to the block forever.
func TestUTXODiffStoreStagedNilChildHidesStoredChild(t *testing.T) {
	dbManager, prefixBucket, teardown := testutils.NewTestDB(t)
	defer teardown()

	store := New(prefixBucket, 10, false)

	blockHash := testutils.Hash(1)
	childHash := testutils.Hash(2)
	diff := utxo.NewUTXODiff()

	stagingArea := model.NewStagingArea()
	store.Stage(stagingArea, blockHash, diff, childHash)
	testutils.Commit(t, dbManager, stagingArea)

	stagingArea = model.NewStagingArea()
	store.Stage(stagingArea, blockHash, diff, nil)

	hasChild, err := store.HasUTXODiffChild(dbManager, stagingArea, blockHash)
	if err != nil {
		t.Fatalf("HasUTXODiffChild: %v", err)
	}
	if hasChild {
		t.Fatalf("expected HasUTXODiffChild to be false after staging no child")
	}
	gotChild, err := store.UTXODiffChild(dbManager, stagingArea, blockHash)
	if err != nil {
		t.Fatalf("UTXODiffChild: %v", err)
	}
	if gotChild != nil {
		t.Fatalf("expected no diff child after staging no child, got the stored child %s", gotChild)
	}
}
