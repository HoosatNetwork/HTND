package headersselectedchainstore

import (
	"testing"

	consensusdb "github.com/HoosatNetwork/HTND/domain/consensus/database"
	"github.com/HoosatNetwork/HTND/domain/consensus/datastructures/testutils"
	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
)

func TestHeadersSelectedChainStoreAddRemoveAndReAdd(t *testing.T) {
	dbManager, prefixBucket, teardown := testutils.NewTestDB(t)
	defer teardown()

	store := New(prefixBucket, 10, false)

	h1 := testutils.Hash(1)
	h2 := testutils.Hash(2)
	h3 := testutils.Hash(3)

	stagingArea := model.NewStagingArea()
	err := store.Stage(dbManager, stagingArea, &externalapi.SelectedChainPath{Added: []*externalapi.DomainHash{h1, h2, h3}, Removed: nil})
	if err != nil {
		t.Fatalf("Stage(add): %v", err)
	}
	if !store.IsStaged(stagingArea) {
		t.Fatalf("expected IsStaged to be true after Stage")
	}
	testutils.Commit(t, dbManager, stagingArea)

	stagingArea = model.NewStagingArea()
	idx, err := store.GetIndexByHash(dbManager, stagingArea, h3)
	if err != nil {
		t.Fatalf("GetIndexByHash: %v", err)
	}
	if idx != 2 {
		t.Fatalf("unexpected index for h3: %d", idx)
	}
	got, err := store.GetHashByIndex(dbManager, stagingArea, 1)
	if err != nil {
		t.Fatalf("GetHashByIndex: %v", err)
	}
	if !got.Equal(h2) {
		t.Fatalf("unexpected hash at index 1")
	}

	// Remove tip
	stagingArea = model.NewStagingArea()
	err = store.Stage(dbManager, stagingArea, &externalapi.SelectedChainPath{Added: nil, Removed: []*externalapi.DomainHash{h3}})
	if err != nil {
		t.Fatalf("Stage(remove): %v", err)
	}
	if !store.IsStaged(stagingArea) {
		t.Fatalf("expected IsStaged to be true after Stage(remove)")
	}
	testutils.Commit(t, dbManager, stagingArea)

	stagingArea = model.NewStagingArea()
	_, err = store.GetIndexByHash(dbManager, stagingArea, h3)
	if err == nil || !consensusdb.IsNotFoundError(err) {
		t.Fatalf("expected not-found for removed tip, got %v", err)
	}
	_, err = store.GetHashByIndex(dbManager, stagingArea, 2)
	if err == nil || !consensusdb.IsNotFoundError(err) {
		t.Fatalf("expected not-found for removed index 2, got %v", err)
	}

	// Re-add a new tip, should reuse index 2
	h4 := testutils.Hash(4)
	stagingArea = model.NewStagingArea()
	err = store.Stage(dbManager, stagingArea, &externalapi.SelectedChainPath{Added: []*externalapi.DomainHash{h4}, Removed: nil})
	if err != nil {
		t.Fatalf("Stage(re-add): %v", err)
	}
	testutils.Commit(t, dbManager, stagingArea)

	stagingArea = model.NewStagingArea()
	idx, err = store.GetIndexByHash(dbManager, stagingArea, h4)
	if err != nil {
		t.Fatalf("GetIndexByHash(h4): %v", err)
	}
	if idx != 2 {
		t.Fatalf("unexpected index for h4: %d", idx)
	}
}
