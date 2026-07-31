package pruningstore

import (
	"testing"

	consensusdb "github.com/HoosatNetwork/HTND/domain/consensus/database"
	"github.com/HoosatNetwork/HTND/domain/consensus/datastructures/testutils"
	"github.com/HoosatNetwork/HTND/domain/consensus/model"
)

func TestPruningStoreCandidateAndPruningPointProgression(t *testing.T) {
	dbManager, prefixBucket, teardown := testutils.NewTestDB(t)
	defer teardown()

	store := New(prefixBucket, 10, false)

	pp0 := testutils.Hash(1)
	pp1 := testutils.Hash(2)
	candidate := testutils.Hash(9)

	stagingArea := model.NewStagingArea()
	store.StagePruningPointCandidate(stagingArea, candidate)
	if err := store.StagePruningPoint(dbManager, stagingArea, pp0); err != nil {
		t.Fatalf("StagePruningPoint(0): %v", err)
	}
	if !store.IsStaged(stagingArea) {
		t.Fatalf("expected IsStaged to be true")
	}
	testutils.Commit(t, dbManager, stagingArea)

	stagingArea = model.NewStagingArea()
	gotCandidate, err := store.PruningPointCandidate(dbManager, stagingArea)
	if err != nil {
		t.Fatalf("PruningPointCandidate: %v", err)
	}
	if !gotCandidate.Equal(candidate) {
		t.Fatalf("unexpected candidate")
	}

	idx, err := store.CurrentPruningPointIndex(dbManager, stagingArea)
	if err != nil {
		t.Fatalf("CurrentPruningPointIndex: %v", err)
	}
	if idx != 0 {
		t.Fatalf("unexpected pruning point index: %d", idx)
	}

	gotPP, err := store.PruningPoint(dbManager, stagingArea)
	if err != nil {
		t.Fatalf("PruningPoint: %v", err)
	}
	if !gotPP.Equal(pp0) {
		t.Fatalf("unexpected pruning point")
	}

	// Advance pruning point
	stagingArea = model.NewStagingArea()
	if err := store.StagePruningPoint(dbManager, stagingArea, pp1); err != nil {
		t.Fatalf("StagePruningPoint(1): %v", err)
	}
	testutils.Commit(t, dbManager, stagingArea)

	stagingArea = model.NewStagingArea()
	idx, err = store.CurrentPruningPointIndex(dbManager, stagingArea)
	if err != nil {
		t.Fatalf("CurrentPruningPointIndex(2): %v", err)
	}
	if idx != 1 {
		t.Fatalf("unexpected pruning point index after advance: %d", idx)
	}

	ppByIndex, err := store.PruningPointByIndex(dbManager, stagingArea, 1)
	if err != nil {
		t.Fatalf("PruningPointByIndex: %v", err)
	}
	if !ppByIndex.Equal(pp1) {
		t.Fatalf("unexpected pruning point at index 1")
	}

	_, err = store.PruningPointByIndex(dbManager, stagingArea, 2)
	if err == nil || !consensusdb.IsNotFoundError(err) {
		t.Fatalf("expected not-found for missing index, got %v", err)
	}
}
