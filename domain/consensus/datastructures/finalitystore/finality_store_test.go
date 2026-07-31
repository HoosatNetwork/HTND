package finalitystore

import (
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus/datastructures/testutils"
	"github.com/HoosatNetwork/HTND/domain/consensus/model"
)

func TestFinalityStoreRoundTrip(t *testing.T) {
	dbManager, prefixBucket, teardown := testutils.NewTestDB(t)
	defer teardown()

	store := New(prefixBucket, 10, false)

	blockHash := testutils.Hash(1)
	finalityPoint := testutils.Hash(7)

	stagingArea := model.NewStagingArea()
	store.StageFinalityPoint(stagingArea, blockHash, finalityPoint)
	if !store.IsStaged(stagingArea) {
		t.Fatalf("expected IsStaged to be true after StageFinalityPoint")
	}
	testutils.Commit(t, dbManager, stagingArea)

	stagingArea = model.NewStagingArea()
	got, err := store.FinalityPoint(dbManager, stagingArea, blockHash)
	if err != nil {
		t.Fatalf("FinalityPoint: %v", err)
	}
	if !got.Equal(finalityPoint) {
		t.Fatalf("unexpected finality point")
	}
}
