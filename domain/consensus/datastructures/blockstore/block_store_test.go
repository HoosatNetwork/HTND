package blockstore

import (
	"math/big"
	"testing"

	"github.com/Hoosat-Oy/HTND/domain/consensus/database"
	"github.com/Hoosat-Oy/HTND/domain/consensus/datastructures/testutils"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/blockheader"
)

func TestBlockStoreRoundTripCountDeleteAndIterator(t *testing.T) {
	dbManager, prefixBucket, teardown := testutils.NewTestDB(t)
	defer teardown()

	storeIface, err := New(dbManager, prefixBucket, 10, false)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	store := storeIface

	hdr := blockheader.NewImmutableBlockHeader(
		1,
		[]externalapi.BlockLevelParents{},
		testutils.Hash(10),
		testutils.Hash(11),
		testutils.Hash(12),
		123,
		0x1d00ffff,
		7,
		100,
		200,
		big.NewInt(0),
		testutils.Hash(13),
	)

	blockHash1 := testutils.Hash(1)
	block1 := &externalapi.DomainBlock{Header: hdr, Transactions: []*externalapi.DomainTransaction{}, PoWHash: "pow1"}

	blockHash2 := testutils.Hash(2)
	block2 := &externalapi.DomainBlock{Header: hdr, Transactions: []*externalapi.DomainTransaction{}, PoWHash: "pow2"}

	stagingArea := model.NewStagingArea()
	store.Stage(stagingArea, blockHash1, block1)
	store.Stage(stagingArea, blockHash2, block2)
	if !store.IsStaged(stagingArea) {
		t.Fatalf("expected IsStaged to be true after Stage")
	}
	if store.Count(stagingArea) != 2 {
		t.Fatalf("unexpected Count before commit: %d", store.Count(stagingArea))
	}
	testutils.Commit(t, dbManager, stagingArea)

	stagingArea = model.NewStagingArea()
	got1, err := store.Block(dbManager, stagingArea, blockHash1)
	if err != nil {
		t.Fatalf("Block: %v", err)
	}
	if !got1.Equal(block1) {
		t.Fatalf("unexpected block1")
	}

	has2, err := store.HasBlock(dbManager, stagingArea, blockHash2)
	if err != nil {
		t.Fatalf("HasBlock: %v", err)
	}
	if !has2 {
		t.Fatalf("expected HasBlock to be true")
	}

	iter, err := store.AllBlockHashesIterator(dbManager)
	if err != nil {
		t.Fatalf("AllBlockHashesIterator: %v", err)
	}
	defer iter.Close()

	seen := map[string]bool{}
	for ok := iter.First(); ok; ok = iter.Next() {
		h, err := iter.Get()
		if err != nil {
			t.Fatalf("iterator.Get: %v", err)
		}
		seen[h.String()] = true
	}
	if !seen[blockHash1.String()] || !seen[blockHash2.String()] {
		t.Fatalf("iterator did not return all expected hashes")
	}

	// Delete one
	stagingArea = model.NewStagingArea()
	store.Delete(stagingArea, blockHash1)
	if !store.IsStaged(stagingArea) {
		t.Fatalf("expected IsStaged to be true after Delete")
	}
	testutils.Commit(t, dbManager, stagingArea)

	stagingArea = model.NewStagingArea()
	_, err = store.Block(dbManager, stagingArea, blockHash1)
	if err == nil || !database.IsNotFoundError(err) {
		t.Fatalf("expected not-found after delete, got %v", err)
	}
	if store.Count(stagingArea) != 1 {
		t.Fatalf("unexpected Count after delete: %d", store.Count(stagingArea))
	}
}
