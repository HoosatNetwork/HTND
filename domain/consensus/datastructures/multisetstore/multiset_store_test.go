package multisetstore

import (
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus/database"
	"github.com/HoosatNetwork/HTND/domain/consensus/datastructures/testutils"
	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/multiset"
)

func TestMultisetStoreRoundTripAndDelete(t *testing.T) {
	dbManager, prefixBucket, teardown := testutils.NewTestDB(t)
	defer teardown()

	store := New(prefixBucket, 10, false)

	blockHash := testutils.Hash(1)
	ms := multiset.New()
	ms.Add([]byte("a"))
	ms.Add([]byte("b"))

	stagingArea := model.NewStagingArea()
	store.Stage(stagingArea, blockHash, ms)
	if !store.IsStaged(stagingArea) {
		t.Fatalf("expected IsStaged to be true after Stage")
	}
	testutils.Commit(t, dbManager, stagingArea)

	stagingArea = model.NewStagingArea()
	got, err := store.Get(dbManager, stagingArea, blockHash)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.Hash().Equal(ms.Hash()) {
		t.Fatalf("unexpected multiset hash. want %s, got %s", ms.Hash(), got.Hash())
	}

	// Delete
	stagingArea = model.NewStagingArea()
	store.Delete(stagingArea, blockHash)
	if !store.IsStaged(stagingArea) {
		t.Fatalf("expected IsStaged to be true after Delete")
	}
	testutils.Commit(t, dbManager, stagingArea)

	stagingArea = model.NewStagingArea()
	_, err = store.Get(dbManager, stagingArea, blockHash)
	if err == nil || !database.IsNotFoundError(err) {
		t.Fatalf("expected not-found after delete, got %v", err)
	}
}
