package syncmanager

import (
	"math/big"
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/processes/ghostdagmanager"
)

// fakeGHOSTDAGDataStore serves canned GHOSTDAG data so a specific blue-work/blue-score arrangement
// can be constructed directly, without needing a DAG whose difficulty actually varies.
type fakeGHOSTDAGDataStore struct {
	data map[externalapi.DomainHash]*externalapi.BlockGHOSTDAGData
}

func (f *fakeGHOSTDAGDataStore) Get(_ model.DBReader, _ *model.StagingArea,
	blockHash *externalapi.DomainHash, _ bool,
) (*externalapi.BlockGHOSTDAGData, error) {
	return f.data[*blockHash], nil
}

func (f *fakeGHOSTDAGDataStore) Stage(*model.StagingArea, *externalapi.DomainHash, *externalapi.BlockGHOSTDAGData, bool) {
}
func (f *fakeGHOSTDAGDataStore) IsStaged(*model.StagingArea) bool { return false }
func (f *fakeGHOSTDAGDataStore) UnstageAll(*model.StagingArea)    {}
func (f *fakeGHOSTDAGDataStore) CacheLen() int                    { return len(f.data) }

// TestSortInTopologicalOrderUsesBlueWorkNotBlueScore pins down why the ordering key must be blue
// work.
//
// Blue score counts blue blocks; blue work accumulates their difficulty. On a branch of many
// low-difficulty blocks a parent can therefore reach a HIGHER blue score than the child that merges
// it, while its blue work stays lower - blue work is strictly increasing from any parent to its
// child, because a block's selected parent is its maximum-blue-work parent and the block's own blue
// work is that plus the work of every blue block it merges.
//
// Ordering a header batch by blue score emits such a pair child-first, and the syncing peer rejects
// the child with ErrMissingParents and aborts the IBD. It only misfires when a diverged-difficulty
// pair lands in the same batch, which is why it hit some nodes and not others.
//
// This case cannot be built from a uniform-difficulty test DAG - there blue score and blue work are
// order-isomorphic and a blue-score sort looks correct - so the GHOSTDAG data is supplied directly.
func TestSortInTopologicalOrderUsesBlueWorkNotBlueScore(t *testing.T) {
	selectedParent := externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{0x01})
	parent := externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{0x02})
	child := externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{0x03})

	ghostdagData := func(blueScore uint64, blueWork int64) *externalapi.BlockGHOSTDAGData {
		return externalapi.NewBlockGHOSTDAGData(blueScore, big.NewInt(blueWork), nil, nil, nil, nil, 0)
	}

	store := &fakeGHOSTDAGDataStore{data: map[externalapi.DomainHash]*externalapi.BlockGHOSTDAGData{
		// The chain the child builds on: little accumulated blue score, lots of work.
		*selectedParent: ghostdagData(10, 140),
		// A merged parent off a long low-difficulty branch: high blue score, less work.
		*parent: ghostdagData(50, 100),
		// The child merges both. Its blue work exceeds every parent's, but its blue score
		// (selectedParent's, plus the two blues it merges) is far below `parent`'s.
		*child: ghostdagData(12, 150),
	}}

	sm := &syncManager{
		ghostdagDataStore: store,
		ghostdagManager:   ghostdagmanager.New(nil, nil, nil, nil, nil, nil, nil, nil),
	}

	// Sanity-check the arrangement is the adversarial one, so this test can't silently degrade into
	// asserting nothing if the numbers are ever edited.
	if store.data[*parent].BlueScore() <= store.data[*child].BlueScore() {
		t.Fatalf("test setup is not adversarial: parent blue score %d must exceed child's %d",
			store.data[*parent].BlueScore(), store.data[*child].BlueScore())
	}
	if store.data[*parent].BlueWork().Cmp(store.data[*child].BlueWork()) >= 0 {
		t.Fatalf("test setup is invalid: parent blue work %s must be below child's %s",
			store.data[*parent].BlueWork(), store.data[*child].BlueWork())
	}

	// Start child-first, the order a blue-score sort would produce and leave untouched.
	hashes := []*externalapi.DomainHash{child, parent, selectedParent}
	if err := sm.sortInTopologicalOrder(model.NewStagingArea(), hashes); err != nil {
		t.Fatalf("sortInTopologicalOrder failed: %v", err)
	}

	positionOf := make(map[externalapi.DomainHash]int, len(hashes))
	for i, hash := range hashes {
		positionOf[*hash] = i
	}
	if positionOf[*parent] > positionOf[*child] {
		t.Fatalf("child %s (index %d) sorted before its parent %s (index %d) - a syncing peer would "+
			"reject it with ErrMissingParents", child, positionOf[*child], parent, positionOf[*parent])
	}
	if positionOf[*selectedParent] > positionOf[*child] {
		t.Fatalf("child %s (index %d) sorted before its selected parent %s (index %d)",
			child, positionOf[*child], selectedParent, positionOf[*selectedParent])
	}
}
