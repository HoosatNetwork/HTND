package blockstatusstore

import (
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus/database"
	"github.com/HoosatNetwork/HTND/domain/consensus/datastructures/testutils"
	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
)

func TestBlockStatusStoreRoundTrip(t *testing.T) {
	dbManager, prefixBucket, teardown := testutils.NewTestDB(t)
	defer teardown()

	store := New(prefixBucket, 10, false)

	stagingArea := model.NewStagingArea()
	blockHash := testutils.Hash(1)
	store.Stage(stagingArea, blockHash, externalapi.StatusUTXOValid)
	testutils.Commit(t, dbManager, stagingArea)

	stagingArea = model.NewStagingArea()
	status, err := store.Get(dbManager, stagingArea, blockHash)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if status != externalapi.StatusUTXOValid {
		t.Fatalf("unexpected status. want %v, got %v", externalapi.StatusUTXOValid, status)
	}

	exists, err := store.Exists(dbManager, stagingArea, blockHash)
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if !exists {
		t.Fatalf("expected Exists to be true")
	}

	missingHash := testutils.Hash(2)
	_, err = store.Get(dbManager, stagingArea, missingHash)
	if err == nil || !database.IsNotFoundError(err) {
		t.Fatalf("expected not-found error for missing hash, got %v", err)
	}
}
