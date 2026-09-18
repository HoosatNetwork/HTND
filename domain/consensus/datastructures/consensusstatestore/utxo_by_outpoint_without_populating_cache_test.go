package consensusstatestore

import (
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus/datastructures/testutils"
	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/utxo"
)

// TestUTXOByOutpointWithoutPopulatingCacheDoesNotEvictTheWorkingSet is HTN-207: a bulk lookup over
// more outpoints than the shared virtual UTXO cache can hold (e.g. an address-balance RPC query)
// used to run every miss through UTXOByOutpoint, which populates the cache on every one of them -
// evicting entries block validation had put there, for no benefit to the bulk lookup itself (it
// walks a fixed list of outpoints once and is never going to revisit the same key before it
// finishes). UTXOByOutpointWithoutPopulatingCache must answer the same way without doing that.
func TestUTXOByOutpointWithoutPopulatingCacheDoesNotEvictTheWorkingSet(t *testing.T) {
	dbManager, prefixBucket, teardown := testutils.NewTestDB(t)
	defer teardown()

	const cacheSize = 2
	store := New(prefixBucket, cacheSize, false)
	css := store.(*consensusStateStore)

	const outpointCount = 6
	outpoints := make([]*externalapi.DomainOutpoint, outpointCount)
	toAdd := map[externalapi.DomainOutpoint]externalapi.UTXOEntry{}
	for i := 0; i < outpointCount; i++ {
		outpoints[i] = testutils.Outpoint(byte(i+1), 0)
		toAdd[*outpoints[i]] = testutils.UTXOEntry(uint64(100+i), 9)
	}
	diff, err := utxo.NewUTXODiffFromCollections(utxo.NewUTXOCollection(toAdd),
		utxo.NewUTXOCollection(map[externalapi.DomainOutpoint]externalapi.UTXOEntry{}))
	if err != nil {
		t.Fatalf("NewUTXODiffFromCollections: %v", err)
	}
	stagingArea := model.NewStagingArea()
	store.StageVirtualUTXODiff(stagingArea, diff)
	testutils.Commit(t, dbManager, stagingArea)
	// Committing populates the cache itself (commitVirtualUTXODiff), which would confound the
	// assertions below - start from a clean cache, as a freshly restarted node would.
	css.virtualUTXOSetCache.Clear()

	// Warm the cache with two entries, as block validation would while it processes new blocks.
	warmed, bulk := outpoints[:2], outpoints[2:]
	stagingArea = model.NewStagingArea()
	for _, outpoint := range warmed {
		if _, ok, err := store.UTXOByOutpoint(dbManager, stagingArea, outpoint); err != nil || !ok {
			t.Fatalf("warming UTXOByOutpoint(%s): ok=%t err=%v", outpoint, ok, err)
		}
	}
	if got := store.CacheLen(); got != cacheSize {
		t.Fatalf("expected the cache to hold %d warmed entries, got %d", cacheSize, got)
	}

	// A bulk lookup over the remaining outpoints - all misses - must answer correctly without
	// growing the cache or evicting what block validation already warmed.
	for _, outpoint := range bulk {
		entry, ok, err := store.UTXOByOutpointWithoutPopulatingCache(dbManager, stagingArea, outpoint)
		if err != nil || !ok {
			t.Fatalf("UTXOByOutpointWithoutPopulatingCache(%s): ok=%t err=%v", outpoint, ok, err)
		}
		if entry == nil {
			t.Fatalf("UTXOByOutpointWithoutPopulatingCache(%s): nil entry with ok=true", outpoint)
		}
	}
	if got := store.CacheLen(); got != cacheSize {
		t.Errorf("a bulk lookup changed the cache size from %d to %d - it must neither grow nor "+
			"shrink it", cacheSize, got)
	}
	for _, outpoint := range warmed {
		if !css.virtualUTXOSetCache.Has(outpoint) {
			t.Errorf("warmed entry %s was evicted by a bulk lookup that must not populate the cache",
				outpoint)
		}
	}
}
