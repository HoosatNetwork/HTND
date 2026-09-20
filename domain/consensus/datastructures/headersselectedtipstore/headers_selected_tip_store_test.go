package headersselectedtipstore

import (
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus/database"
	"github.com/HoosatNetwork/HTND/domain/consensus/datastructures/testutils"
	"github.com/HoosatNetwork/HTND/domain/consensus/model"
)

func TestHeadersSelectedTipStoreRoundTrip(t *testing.T) {
	dbManager, prefixBucket, teardown := testutils.NewTestDB(t)
	defer teardown()

	store := New(prefixBucket)

	stagingArea := model.NewStagingArea()
	_, err := store.HeadersSelectedTip(dbManager, stagingArea)
	if err == nil || !database.IsNotFoundError(err) {
		t.Fatalf("expected not-found before stage, got %v", err)
	}

	tip := testutils.Hash(1)
	store.Stage(stagingArea, tip)
	if !store.IsStaged(stagingArea) {
		t.Fatalf("expected IsStaged to be true after Stage")
	}
	testutils.Commit(t, dbManager, stagingArea)

	stagingArea = model.NewStagingArea()
	has, err := store.Has(dbManager, stagingArea)
	if err != nil {
		t.Fatalf("Has: %v", err)
	}
	if !has {
		t.Fatalf("expected Has to be true")
	}

	got, err := store.HeadersSelectedTip(dbManager, stagingArea)
	if err != nil {
		t.Fatalf("HeadersSelectedTip: %v", err)
	}
	if !got.Equal(tip) {
		t.Fatalf("unexpected tip")
	}
}
