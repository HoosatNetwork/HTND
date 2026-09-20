package consensusstatestore

import (
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus/datastructures/testutils"
	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/utxo"
)

func TestConsensusStateStoreTipsAndVirtualUTXOSet(t *testing.T) {
	dbManager, prefixBucket, teardown := testutils.NewTestDB(t)
	defer teardown()

	store := New(prefixBucket, 10, false)

	// Tips
	tips := []*externalapi.DomainHash{testutils.Hash(1), testutils.Hash(2)}
	stagingArea := model.NewStagingArea()
	store.StageTips(stagingArea, tips)
	if !store.IsStaged(stagingArea) {
		t.Fatalf("expected IsStaged to be true after StageTips")
	}
	testutils.Commit(t, dbManager, stagingArea)

	stagingArea = model.NewStagingArea()
	gotTips, err := store.Tips(stagingArea, dbManager)
	if err != nil {
		t.Fatalf("Tips: %v", err)
	}
	if len(gotTips) != len(tips) || !gotTips[0].Equal(tips[0]) || !gotTips[1].Equal(tips[1]) {
		t.Fatalf("unexpected tips")
	}

	// Virtual UTXO diff
	outpoint := testutils.Outpoint(1, 0)
	entry := testutils.UTXOEntry(123, 9)
	toAdd := utxo.NewUTXOCollection(map[externalapi.DomainOutpoint]externalapi.UTXOEntry{*outpoint: entry})
	toRemove := utxo.NewUTXOCollection(map[externalapi.DomainOutpoint]externalapi.UTXOEntry{})
	diff, err := utxo.NewUTXODiffFromCollections(toAdd, toRemove)
	if err != nil {
		t.Fatalf("NewUTXODiffFromCollections: %v", err)
	}

	stagingArea = model.NewStagingArea()
	store.StageVirtualUTXODiff(stagingArea, diff)
	if !store.IsStaged(stagingArea) {
		t.Fatalf("expected IsStaged to be true after StageVirtualUTXODiff")
	}
	testutils.Commit(t, dbManager, stagingArea)

	stagingArea = model.NewStagingArea()
	has, err := store.HasUTXOByOutpoint(dbManager, stagingArea, outpoint)
	if err != nil {
		t.Fatalf("HasUTXOByOutpoint: %v", err)
	}
	if !has {
		t.Fatalf("expected HasUTXOByOutpoint to be true")
	}

	gotEntry, ok, err := store.UTXOByOutpoint(dbManager, stagingArea, outpoint)
	if err != nil {
		t.Fatalf("UTXOByOutpoint: %v", err)
	}
	if !ok {
		t.Fatalf("expected UTXOByOutpoint ok=true")
	}
	if !gotEntry.Equal(entry) {
		t.Fatalf("unexpected utxo entry")
	}
}

func TestConsensusStateStoreImportPruningPointUTXOSetFlow(t *testing.T) {
	dbManager, prefixBucket, teardown := testutils.NewTestDB(t)
	defer teardown()

	store := New(prefixBucket, 10, false)

	outpoint := testutils.Outpoint(2, 1)
	entry := testutils.UTXOEntry(555, 11)
	collection := utxo.NewUTXOCollection(map[externalapi.DomainOutpoint]externalapi.UTXOEntry{*outpoint: entry})
	iter := collection.Iterator()
	defer iter.Close()

	had, err := store.HadStartedImportingPruningPointUTXOSet(dbManager)
	if err != nil {
		t.Fatalf("HadStartedImportingPruningPointUTXOSet: %v", err)
	}
	if had {
		t.Fatalf("expected had=false initially")
	}

	if err := store.StartImportingPruningPointUTXOSet(dbManager); err != nil {
		t.Fatalf("StartImportingPruningPointUTXOSet: %v", err)
	}

	if err := store.ImportPruningPointUTXOSetIntoVirtualUTXOSet(dbManager, iter); err != nil {
		t.Fatalf("ImportPruningPointUTXOSetIntoVirtualUTXOSet: %v", err)
	}

	if err := store.FinishImportingPruningPointUTXOSet(dbManager); err != nil {
		t.Fatalf("FinishImportingPruningPointUTXOSet: %v", err)
	}

	stagingArea := model.NewStagingArea()
	gotEntry, ok, err := store.UTXOByOutpoint(dbManager, stagingArea, outpoint)
	if err != nil {
		t.Fatalf("UTXOByOutpoint: %v", err)
	}
	if !ok {
		t.Fatalf("expected ok=true")
	}
	if !gotEntry.Equal(entry) {
		t.Fatalf("unexpected imported entry")
	}
}
