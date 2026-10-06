package blockstatusstore

import (
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/datastructures/testutils"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
)

// TestGetWithoutCachingReturnsTheLastCommittedStatus pins that the status read used without the
// consensus lock reports a missing block as not existing rather than as an error, returns the status
// of the last commit as the block's status changes, and neither hits nor fills the cache, which is
// not safe for concurrent use.
func TestGetWithoutCachingReturnsTheLastCommittedStatus(t *testing.T) {
	dbManager, prefixBucket, teardown := testutils.NewTestDB(t)
	defer teardown()

	store := New(prefixBucket, 10, false).(*blockStatusStore)
	blockHash := testutils.Hash(1)

	_, exists, err := store.GetWithoutCaching(dbManager, blockHash)
	if err != nil || exists {
		t.Fatalf("GetWithoutCaching of a missing block = exists %t, %v; want false, nil", exists, err)
	}

	for _, status := range []externalapi.BlockStatus{
		externalapi.StatusUTXOPendingVerification, externalapi.StatusUTXOValid,
	} {
		stagingArea := model.NewStagingArea()
		store.Stage(stagingArea, blockHash, status)
		testutils.Commit(t, dbManager, stagingArea)
		store.ClearCache()

		got, exists, err := store.GetWithoutCaching(dbManager, blockHash)
		if err != nil {
			t.Fatalf("GetWithoutCaching: %v", err)
		}
		if !exists || got != status {
			t.Errorf("GetWithoutCaching = %s, exists %t; want %s, true", got, exists, status)
		}
		if store.cache.Len() != 0 {
			t.Errorf("GetWithoutCaching left %d statuses in the cache, want 0", store.cache.Len())
		}
	}
}
