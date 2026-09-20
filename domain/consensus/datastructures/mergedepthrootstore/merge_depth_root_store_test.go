package mergedepthrootstore

import (
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus/datastructures/testutils"
	"github.com/HoosatNetwork/HTND/domain/consensus/model"
)

func TestMergeDepthRootStoreRoundTrip(t *testing.T) {
	dbManager, prefixBucket, teardown := testutils.NewTestDB(t)
	defer teardown()

	store := New(prefixBucket, 10, false)

	blockHash := testutils.Hash(1)
	rootHash := testutils.Hash(9)

	stagingArea := model.NewStagingArea()
	store.StageMergeDepthRoot(stagingArea, blockHash, rootHash)
	if !store.IsStaged(stagingArea) {
		t.Fatalf("expected IsStaged to be true after StageMergeDepthRoot")
	}
	testutils.Commit(t, dbManager, stagingArea)

	stagingArea = model.NewStagingArea()
	got, err := store.MergeDepthRoot(dbManager, stagingArea, blockHash)
	if err != nil {
		t.Fatalf("MergeDepthRoot: %v", err)
	}
	if !got.Equal(rootHash) {
		t.Fatalf("unexpected root")
	}
}
