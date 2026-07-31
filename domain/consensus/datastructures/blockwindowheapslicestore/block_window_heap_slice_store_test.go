package blockwindowheapslicestore

import (
	"math/big"
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus/datastructures/testutils"
	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/infrastructure/db/database"
)

func TestBlockWindowHeapSliceStoreStageCommitAndGet(t *testing.T) {
	dbManager, _, teardown := testutils.NewTestDB(t)
	defer teardown()

	store := New(10, false)

	blockHash := testutils.Hash(1)
	windowSize := 5
	pair := &externalapi.BlockGHOSTDAGDataHashPair{
		Hash: testutils.Hash(2),
		GHOSTDAGData: externalapi.NewBlockGHOSTDAGData(
			7,
			big.NewInt(0),
			testutils.Hash(3),
			[]*externalapi.DomainHash{testutils.Hash(4)},
			[]*externalapi.DomainHash{},
			map[externalapi.DomainHash]externalapi.KType{},
			externalapi.KType(1),
		),
	}
	heapSlice := []*externalapi.BlockGHOSTDAGDataHashPair{pair}

	stagingArea := model.NewStagingArea()
	store.Stage(stagingArea, blockHash, windowSize, heapSlice)
	if !store.IsStaged(stagingArea) {
		t.Fatalf("expected IsStaged to be true after Stage")
	}
	testutils.Commit(t, dbManager, stagingArea)

	stagingArea = model.NewStagingArea()
	got, err := store.Get(stagingArea, blockHash, windowSize)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got) != 1 || !got[0].Hash.Equal(pair.Hash) {
		t.Fatalf("unexpected heap slice")
	}

	_, err = store.Get(stagingArea, testutils.Hash(9), windowSize)
	if err == nil || !database.IsNotFoundError(err) {
		t.Fatalf("expected not-found for missing heap slice, got %v", err)
	}
}
