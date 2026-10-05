package blockstore

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

// TestBlockWithoutCachingNeverPutsABlockInTheCache pins that the read used without the consensus lock
// serves committed blocks, from the cache on a hit, and never adds to the cache. A lock-free read
// that cached what it read could put back a block a concurrent pruning commit had just deleted,
// and HasBlock, which answers from the cache first, would then report the pruned body as present.
func TestBlockWithoutCachingNeverPutsABlockInTheCache(t *testing.T) {
	dbManager, prefixBucket, teardown := testutils.NewTestDB(t)
	defer teardown()

	storeIface, err := New(dbManager, prefixBucket, 10, 1<<20, false)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	store := storeIface.(*blockStore)

	header := blockheader.NewImmutableBlockHeader(1, []externalapi.BlockLevelParents{}, testutils.Hash(10),
		testutils.Hash(11), testutils.Hash(12), 123, 0x1d00ffff, 7, 100, 200, big.NewInt(0), testutils.Hash(13))
	stored := testutils.Hash(1)
	stagingArea := model.NewStagingArea()
	store.Stage(stagingArea, stored, &externalapi.DomainBlock{
		Header: header, Transactions: []*externalapi.DomainTransaction{}, PoWHash: "pow",
	})
	testutils.Commit(t, dbManager, stagingArea)

	// A cache hit: the commit cached the block, and the read returns it with the PoW hash only the
	// cache keeps.
	block, err := store.BlockWithoutCaching(dbManager, stored)
	if err != nil {
		t.Fatalf("BlockWithoutCaching of a cached block: %v", err)
	}
	if block.PoWHash != "pow" {
		t.Errorf("BlockWithoutCaching of a cached block returned PoW hash %q, want %q", block.PoWHash, "pow")
	}

	// A cache miss: served from the database, and the cache stays empty.
	store.cache.Clear()
	block, err = store.BlockWithoutCaching(dbManager, stored)
	if err != nil {
		t.Fatalf("BlockWithoutCaching of an uncached block: %v", err)
	}
	if !block.Header.Equal(header) {
		t.Errorf("BlockWithoutCaching returned header %v, want %v", block.Header, header)
	}
	if store.cache.Len() != 0 {
		t.Errorf("BlockWithoutCaching left %d blocks in the cache, want 0", store.cache.Len())
	}

	// HasBlockWithoutCaching answers from the database and remembers nothing.
	has, err := store.HasBlockWithoutCaching(dbManager, stored)
	if err != nil {
		t.Fatalf("HasBlockWithoutCaching: %v", err)
	}
	if !has {
		t.Errorf("HasBlockWithoutCaching reports a stored block as absent")
	}
	if store.existsCache.Len() != 0 {
		t.Errorf("HasBlockWithoutCaching left %d blocks in existsCache, want 0", store.existsCache.Len())
	}

	// After a pruning commit deletes the block, neither read reports it present: nothing above left
	// it in a cache for HasBlock to find.
	stagingArea = model.NewStagingArea()
	store.Delete(stagingArea, stored)
	testutils.Commit(t, dbManager, stagingArea)
	_, err = store.BlockWithoutCaching(dbManager, stored)
	if !errors.Is(err, database.ErrNotFound) {
		t.Errorf("BlockWithoutCaching of a deleted block returned %v, want ErrNotFound", err)
	}
	has, err = store.HasBlock(dbManager, model.NewStagingArea(), stored)
	if err != nil {
		t.Fatalf("HasBlock: %v", err)
	}
	if has {
		t.Errorf("HasBlock reports a deleted block as present")
	}
	if has, err := store.HasBlockWithoutCaching(dbManager, stored); err != nil || has {
		t.Errorf("HasBlockWithoutCaching of a deleted block = %t, %v; want false, nil", has, err)
	}
}
