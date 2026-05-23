package ghostdagdatastore

import (
	"math/big"
	"testing"

	"github.com/Hoosat-Oy/HTND/domain/consensus/datastructures/testutils"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
)

func TestGHOSTDAGDataStoreRoundTripTrustedAndUntrusted(t *testing.T) {
	dbManager, prefixBucket, teardown := testutils.NewTestDB(t)
	defer teardown()

	store := New(prefixBucket, 10, false)

	blockHash := testutils.Hash(1)
	data := externalapi.NewBlockGHOSTDAGData(
		7,
		big.NewInt(123),
		testutils.Hash(2),
		[]*externalapi.DomainHash{testutils.Hash(3)},
		[]*externalapi.DomainHash{testutils.Hash(4)},
		map[externalapi.DomainHash]externalapi.KType{*testutils.Hash(3): 1},
		externalapi.KType(1),
	)

	stagingArea := model.NewStagingArea()
	store.Stage(stagingArea, blockHash, data, false)
	store.Stage(stagingArea, blockHash, data, true)
	if !store.IsStaged(stagingArea) {
		t.Fatalf("expected IsStaged to be true after Stage")
	}
	testutils.Commit(t, dbManager, stagingArea)

	stagingArea = model.NewStagingArea()
	gotUntrusted, err := store.Get(dbManager, stagingArea, blockHash, false)
	if err != nil {
		t.Fatalf("Get(untrusted): %v", err)
	}
	if gotUntrusted.BlueScore() != data.BlueScore() || !gotUntrusted.SelectedParent().Equal(data.SelectedParent()) {
		t.Fatalf("unexpected untrusted data")
	}
	if gotUntrusted.DynamicK() != data.DynamicK() {
		t.Fatalf("unexpected untrusted dynamic K: got %d, want %d", gotUntrusted.DynamicK(), data.DynamicK())
	}

	gotTrusted, err := store.Get(dbManager, stagingArea, blockHash, true)
	if err != nil {
		t.Fatalf("Get(trusted): %v", err)
	}
	if gotTrusted.BlueScore() != data.BlueScore() || !gotTrusted.SelectedParent().Equal(data.SelectedParent()) {
		t.Fatalf("unexpected trusted data")
	}
	if gotTrusted.DynamicK() != data.DynamicK() {
		t.Fatalf("unexpected trusted dynamic K: got %d, want %d", gotTrusted.DynamicK(), data.DynamicK())
	}
}
