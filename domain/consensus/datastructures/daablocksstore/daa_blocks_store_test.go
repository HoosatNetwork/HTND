package daablocksstore

import (
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus/datastructures/testutils"
	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
)

func TestDAABlocksStoreRoundTripAndDelete(t *testing.T) {
	dbManager, prefixBucket, teardown := testutils.NewTestDB(t)
	defer teardown()

	store := New(prefixBucket, 10, 10, false)

	blockHash := testutils.Hash(1)
	added := []*externalapi.DomainHash{testutils.Hash(2), testutils.Hash(3)}

	stagingArea := model.NewStagingArea()
	store.StageDAAScore(stagingArea, blockHash, 12345)
	store.StageBlockDAAAddedBlocks(stagingArea, blockHash, added)
	if !store.IsStaged(stagingArea) {
		t.Fatalf("expected IsStaged to be true after Stage")
	}
	testutils.Commit(t, dbManager, stagingArea)

	stagingArea = model.NewStagingArea()
	score, err := store.DAAScore(dbManager, stagingArea, blockHash)
	if err != nil {
		t.Fatalf("DAAScore: %v", err)
	}
	if score != 12345 {
		t.Fatalf("unexpected score: %d", score)
	}

	gotAdded, err := store.DAAAddedBlocks(dbManager, stagingArea, blockHash)
	if err != nil {
		t.Fatalf("DAAAddedBlocks: %v", err)
	}
	if len(gotAdded) != len(added) || !gotAdded[0].Equal(added[0]) || !gotAdded[1].Equal(added[1]) {
		t.Fatalf("unexpected added blocks")
	}

	// Delete
	stagingArea = model.NewStagingArea()
	store.Delete(stagingArea, blockHash)
	if !store.IsStaged(stagingArea) {
		t.Fatalf("expected IsStaged to be true after Delete")
	}
	testutils.Commit(t, dbManager, stagingArea)
}
