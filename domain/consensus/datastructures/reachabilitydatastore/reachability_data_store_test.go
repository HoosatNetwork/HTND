package reachabilitydatastore

import (
	"testing"

	consensusdb "github.com/Hoosat-Oy/HTND/domain/consensus/database"
	"github.com/Hoosat-Oy/HTND/domain/consensus/datastructures/testutils"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/reachabilitydata"
)

func TestReachabilityDataStoreRoundTripHasAndDelete(t *testing.T) {
	dbManager, prefixBucket, teardown := testutils.NewTestDB(t)
	defer teardown()

	store := New(prefixBucket, 10, false)

	blockHash := testutils.Hash(1)
	root := testutils.Hash(2)
	data := reachabilitydata.New(
		[]*externalapi.DomainHash{testutils.Hash(3)},
		testutils.Hash(4),
		&model.ReachabilityInterval{Start: 1, End: 2},
		model.FutureCoveringTreeNodeSet{},
	)

	stagingArea := model.NewStagingArea()
	store.StageReachabilityData(stagingArea, blockHash, data)
	store.StageReachabilityReindexRoot(stagingArea, root)
	if !store.IsStaged(stagingArea) {
		t.Fatalf("expected IsStaged to be true after stage")
	}
	testutils.Commit(t, dbManager, stagingArea)

	stagingArea = model.NewStagingArea()
	has, err := store.HasReachabilityData(dbManager, stagingArea, blockHash)
	if err != nil {
		t.Fatalf("HasReachabilityData: %v", err)
	}
	if !has {
		t.Fatalf("expected HasReachabilityData to be true")
	}

	got, err := store.ReachabilityData(dbManager, stagingArea, blockHash)
	if err != nil {
		t.Fatalf("ReachabilityData: %v", err)
	}
	if !got.Equal(data) {
		t.Fatalf("unexpected reachability data")
	}

	gotRoot, err := store.ReachabilityReindexRoot(dbManager, stagingArea)
	if err != nil {
		t.Fatalf("ReachabilityReindexRoot: %v", err)
	}
	if !gotRoot.Equal(root) {
		t.Fatalf("unexpected reachability reindex root")
	}

	// Delete clears all data directly from the DB
	dbTx, err := dbManager.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer func() {
		if err := dbTx.RollbackUnlessClosed(); err != nil {
			t.Fatalf("dbTx.RollbackUnlessClosed: %v", err)
		}
	}()
	if err := store.Delete(dbTx); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := dbTx.Commit(); err != nil {
		t.Fatalf("Commit tx: %v", err)
	}

	// Recreate store to avoid stale in-memory cache after Delete.
	store = New(prefixBucket, 10, false)

	stagingArea = model.NewStagingArea()
	has, err = store.HasReachabilityData(dbManager, stagingArea, blockHash)
	if err != nil {
		t.Fatalf("HasReachabilityData after delete: %v", err)
	}
	if has {
		t.Fatalf("expected HasReachabilityData to be false after delete")
	}
	_, err = store.ReachabilityReindexRoot(dbManager, stagingArea)
	if err == nil || !consensusdb.IsNotFoundError(err) {
		t.Fatalf("expected not-found for reindex root after delete, got %v", err)
	}
}
