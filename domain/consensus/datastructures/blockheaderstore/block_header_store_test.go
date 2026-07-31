package blockheaderstore

import (
	"math/big"
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus/database"
	"github.com/HoosatNetwork/HTND/domain/consensus/datastructures/testutils"
	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/blockheader"
)

func TestBlockHeaderStoreRoundTripCountAndDelete(t *testing.T) {
	dbManager, prefixBucket, teardown := testutils.NewTestDB(t)
	defer teardown()

	storeIface, err := New(dbManager, prefixBucket, 10, false)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	store := storeIface

	blockHash := testutils.Hash(1)
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

	stagingArea := model.NewStagingArea()
	store.Stage(stagingArea, blockHash, hdr)
	if !store.IsStaged(stagingArea) {
		t.Fatalf("expected IsStaged to be true after Stage")
	}
	if store.Count(stagingArea) != 1 {
		t.Fatalf("unexpected Count before commit: %d", store.Count(stagingArea))
	}
	testutils.Commit(t, dbManager, stagingArea)

	stagingArea = model.NewStagingArea()
	has, err := store.HasBlockHeader(dbManager, stagingArea, blockHash)
	if err != nil {
		t.Fatalf("HasBlockHeader: %v", err)
	}
	if !has {
		t.Fatalf("expected HasBlockHeader to be true")
	}

	got, err := store.BlockHeader(dbManager, stagingArea, blockHash)
	if err != nil {
		t.Fatalf("BlockHeader: %v", err)
	}
	if !got.Equal(hdr) {
		t.Fatalf("unexpected header")
	}
	if store.Count(stagingArea) != 1 {
		t.Fatalf("unexpected Count after commit: %d", store.Count(stagingArea))
	}

	// Delete
	stagingArea = model.NewStagingArea()
	store.Delete(stagingArea, blockHash)
	if !store.IsStaged(stagingArea) {
		t.Fatalf("expected IsStaged to be true after Delete")
	}
	testutils.Commit(t, dbManager, stagingArea)

	stagingArea = model.NewStagingArea()
	_, err = store.BlockHeader(dbManager, stagingArea, blockHash)
	if err == nil || !database.IsNotFoundError(err) {
		t.Fatalf("expected not-found after delete, got %v", err)
	}
}
