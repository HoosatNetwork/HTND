package blockrelationstore

import (
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus/datastructures/testutils"
	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
)

func TestBlockRelationStoreRoundTrip(t *testing.T) {
	dbManager, prefixBucket, teardown := testutils.NewTestDB(t)
	defer teardown()

	store := New(prefixBucket, 10, false)

	blockHash := testutils.Hash(1)
	relations := &model.BlockRelations{Parents: []*externalapi.DomainHash{testutils.Hash(2)}, Children: []*externalapi.DomainHash{testutils.Hash(3)}}

	stagingArea := model.NewStagingArea()
	store.StageBlockRelation(stagingArea, blockHash, relations)
	if !store.IsStaged(stagingArea) {
		t.Fatalf("expected IsStaged to be true after StageBlockRelation")
	}
	testutils.Commit(t, dbManager, stagingArea)

	stagingArea = model.NewStagingArea()
	has, err := store.Has(dbManager, stagingArea, blockHash)
	if err != nil {
		t.Fatalf("Has: %v", err)
	}
	if !has {
		t.Fatalf("expected Has to be true")
	}

	got, err := store.BlockRelation(dbManager, stagingArea, blockHash)
	if err != nil {
		t.Fatalf("BlockRelation: %v", err)
	}
	if !got.Equal(relations) {
		t.Fatalf("unexpected relations")
	}
}
