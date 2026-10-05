package blockheaderstore

import (
	"errors"
	"math/big"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/database"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/datastructures/testutils"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/blockheader"
)

// TestBlockHeaderWithoutCachingNeverTouchesTheCache pins that the header read used without the
// consensus lock reads the database alone. The header cache is an lrucache.LRUCache, which is not
// safe for concurrent use - even a Get reorders it - so a lock-free reader that hit or filled it
// would race block processing.
func TestBlockHeaderWithoutCachingNeverTouchesTheCache(t *testing.T) {
	dbManager, prefixBucket, teardown := testutils.NewTestDB(t)
	defer teardown()

	storeIface, err := New(dbManager, prefixBucket, 10, false)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	store := storeIface.(*blockHeaderStore)

	header := blockheader.NewImmutableBlockHeader(1, []externalapi.BlockLevelParents{}, testutils.Hash(10),
		testutils.Hash(11), testutils.Hash(12), 123, 0x1d00ffff, 7, 100, 200, big.NewInt(0), testutils.Hash(13))
	stored := testutils.Hash(1)
	stagingArea := model.NewStagingArea()
	store.Stage(stagingArea, stored, header)
	testutils.Commit(t, dbManager, stagingArea)
	cachedAfterCommit := store.cache.Len()

	got, err := store.BlockHeaderWithoutCaching(dbManager, stored)
	if err != nil {
		t.Fatalf("BlockHeaderWithoutCaching: %v", err)
	}
	if !got.Equal(header) {
		t.Errorf("BlockHeaderWithoutCaching returned %v, want %v", got, header)
	}

	// Read from the database even with the cache empty, and leave it empty.
	store.cache.Clear()
	if _, err := store.BlockHeaderWithoutCaching(dbManager, stored); err != nil {
		t.Fatalf("BlockHeaderWithoutCaching of an uncached header: %v", err)
	}
	if store.cache.Len() != 0 {
		t.Errorf("BlockHeaderWithoutCaching left %d headers in the cache (the commit left %d), want 0",
			store.cache.Len(), cachedAfterCommit)
	}

	stagingArea = model.NewStagingArea()
	store.Delete(stagingArea, stored)
	testutils.Commit(t, dbManager, stagingArea)
	if _, err := store.BlockHeaderWithoutCaching(dbManager, stored); !errors.Is(err, database.ErrNotFound) {
		t.Errorf("BlockHeaderWithoutCaching of a deleted header returned %v, want ErrNotFound", err)
	}
}
