package daawindowstore

import (
	"math/big"
	"testing"

	"github.com/Hoosat-Oy/HTND/domain/consensus/datastructures/testutils"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
)

func TestDAAWindowStoreRoundTrip(t *testing.T) {
	dbManager, prefixBucket, teardown := testutils.NewTestDB(t)
	defer teardown()

	store := New(prefixBucket, 10, false)

	blockHash := testutils.Hash(1)
	index := uint64(0)
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

	stagingArea := model.NewStagingArea()
	store.Stage(stagingArea, blockHash, index, pair)
	if !store.IsStaged(stagingArea) {
		t.Fatalf("expected IsStaged to be true after Stage")
	}
	testutils.Commit(t, dbManager, stagingArea)

	stagingArea = model.NewStagingArea()
	got, err := store.DAAWindowBlock(dbManager, stagingArea, blockHash, index)
	if err != nil {
		t.Fatalf("DAAWindowBlock: %v", err)
	}
	if !got.Hash.Equal(pair.Hash) || got.GHOSTDAGData.BlueScore() != pair.GHOSTDAGData.BlueScore() {
		t.Fatalf("unexpected pair")
	}
}
