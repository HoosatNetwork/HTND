package blockstore

import (
	"math/big"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/datastructures/testutils"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/blockheader"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/subnetworks"
)

// TestBlockCacheStaysWithinByteBudget pins that the block cache is bounded by the size of what it
// holds, not only by its entry count, that blocks evicted from it are still read from the database,
// and that a cache hit returns an equal, separate block with its PoW hash. With ML-DSA-44 inputs a
// count-bounded cache of 10,000 testnet blocks held over 3 GB of decoded inputs, which pinned the heap
// at GOMEMLIMIT.
func TestBlockCacheStaysWithinByteBudget(t *testing.T) {
	dbManager, prefixBucket, teardown := testutils.NewTestDB(t)
	defer teardown()

	// Each block carries one input with an ML-DSA-sized (~3.7 KB) signature script and weighs one
	// 4 KB page, so a 10 KB budget holds two blocks although the count limit would allow ten.
	const budget = 10_000
	storeIface, err := New(dbManager, prefixBucket, 10, budget, false)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	store := storeIface.(*blockStore)

	header := blockheader.NewImmutableBlockHeader(1, []externalapi.BlockLevelParents{}, testutils.Hash(10),
		testutils.Hash(11), testutils.Hash(12), 123, 0x1d00ffff, 7, 100, 200, big.NewInt(0), testutils.Hash(13))
	powHashes := []string{"pow0", "pow1", "pow2", "pow3", "pow4", "pow5"}
	newBlock := func(i int) *externalapi.DomainBlock {
		return &externalapi.DomainBlock{
			Header: header,
			Transactions: []*externalapi.DomainTransaction{{
				Inputs: []*externalapi.DomainTransactionInput{{
					PreviousOutpoint: externalapi.DomainOutpoint{
						TransactionID: externalapi.DomainTransactionID(*testutils.Hash(byte(i))),
					},
					SignatureScript: make([]byte, 3700),
					SigOpCount:      1,
				}},
				Outputs:      []*externalapi.DomainTransactionOutput{},
				SubnetworkID: subnetworks.SubnetworkIDNative,
				Payload:      []byte{},
			}},
			PoWHash: powHashes[i],
		}
	}

	const blockCount = 6
	hashes := make([]*externalapi.DomainHash, blockCount)
	blocks := make([]*externalapi.DomainBlock, blockCount)
	for i := range blockCount {
		hashes[i] = testutils.Hash(byte(100 + i))
		blocks[i] = newBlock(i)
		stagingArea := model.NewStagingArea()
		store.Stage(stagingArea, hashes[i], blocks[i])
		testutils.Commit(t, dbManager, stagingArea)
		if store.cache.Bytes() > budget {
			t.Fatalf("after %d commits the cache weighs %d bytes, over its budget", i+1, store.cache.Bytes())
		}
	}
	if store.CacheLen() != 2 {
		t.Fatalf("cache holds %d blocks, want the 2 its budget allows", store.CacheLen())
	}

	// The last two committed blocks are cached and come back whole, PoW hash included, as separate
	// copies.
	for i := blockCount - 2; i < blockCount; i++ {
		first, err := store.Block(dbManager, model.NewStagingArea(), hashes[i])
		if err != nil {
			t.Fatalf("Block %d: %v", i, err)
		}
		second, err := store.Block(dbManager, model.NewStagingArea(), hashes[i])
		if err != nil {
			t.Fatalf("Block %d: %v", i, err)
		}
		if !first.Equal(blocks[i]) || first.PoWHash != blocks[i].PoWHash || first == second {
			t.Fatalf("cached block %d is not an equal, separate copy with its PoW hash", i)
		}
	}

	// An evicted block is read from the database, which does not store the PoW hash.
	evicted, err := store.Block(dbManager, model.NewStagingArea(), hashes[0])
	if err != nil {
		t.Fatalf("Block 0: %v", err)
	}
	if evicted.PoWHash != "" {
		t.Fatalf("a block read from the database has PoW hash %q", evicted.PoWHash)
	}
	evicted.PoWHash = blocks[0].PoWHash
	if !evicted.Equal(blocks[0]) {
		t.Fatalf("evicted block did not round-trip through the database")
	}
	if store.cache.Bytes() > budget {
		t.Fatalf("reading blocks back pushed the cache to %d bytes", store.cache.Bytes())
	}

	// Deleting a cached block drops it from the cache.
	stagingArea := model.NewStagingArea()
	store.Delete(stagingArea, hashes[0])
	testutils.Commit(t, dbManager, stagingArea)
	if store.cache.Has(hashes[0]) {
		t.Fatalf("a deleted block is still cached")
	}
}
