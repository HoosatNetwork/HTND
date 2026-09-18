package reachabilitymanager

import (
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus/database"
	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/pkg/errors"
)

// ghostdagDataStoreMock serves GHOSTDAG data for the blocks it was given and
// database.ErrNotFound for every other block, the way the real store behaves for
// a block that entered the reachability tree through the pruning proof and never
// had its level-0 GHOSTDAG data built.
type ghostdagDataStoreMock struct {
	data map[externalapi.DomainHash]*externalapi.BlockGHOSTDAGData
}

func newGHOSTDAGDataStoreMock() *ghostdagDataStoreMock {
	return &ghostdagDataStoreMock{data: make(map[externalapi.DomainHash]*externalapi.BlockGHOSTDAGData)}
}

func (g *ghostdagDataStoreMock) Stage(_ *model.StagingArea, blockHash *externalapi.DomainHash,
	blockGHOSTDAGData *externalapi.BlockGHOSTDAGData, _ bool,
) {
	g.data[*blockHash] = blockGHOSTDAGData
}

func (g *ghostdagDataStoreMock) IsStaged(*model.StagingArea) bool {
	return len(g.data) != 0
}

func (g *ghostdagDataStoreMock) Get(_ model.DBReader, _ *model.StagingArea, blockHash *externalapi.DomainHash,
	_ bool,
) (*externalapi.BlockGHOSTDAGData, error) {
	blockGHOSTDAGData, ok := g.data[*blockHash]
	if !ok {
		return nil, errors.Wrapf(database.ErrNotFound, "GHOSTDAG data for block %s not found", blockHash)
	}
	return blockGHOSTDAGData, nil
}

func (g *ghostdagDataStoreMock) UnstageAll(*model.StagingArea) {}

func (g *ghostdagDataStoreMock) CacheLen() int { return len(g.data) }

func (g *ghostdagDataStoreMock) stageBlueScore(blockHash *externalapi.DomainHash, blueScore uint64) {
	g.data[*blockHash] = externalapi.NewBlockGHOSTDAGData(blueScore, nil, nil, nil, nil, nil, 0)
}

// TestUpdateReindexRootPassesBlocksWithoutGHOSTDAGData builds the shape a node is left in
// after a pruning-proof IBD: the reindex root is the virtual genesis marker, and the
// reachability tree child that leads towards the selected tip is a block that only ever
// existed above level 0, so it has no level-0 GHOSTDAG data. The reindex root has to keep
// descending past such a block - if it stops there it stays at the root of the whole tree
// and every subsequent block reindexes every node under it.
func TestUpdateReindexRootPassesBlocksWithoutGHOSTDAGData(t *testing.T) {
	reachabilityDataStore := newReachabilityDataStoreMock()
	ghostdagDataStore := newGHOSTDAGDataStoreMock()
	manager := New(nil, ghostdagDataStore, reachabilityDataStore).(*reachabilityManager)
	manager.reindexWindow = 10
	helper := newTestHelper(manager, t, reachabilityDataStore)

	stagingArea := model.NewStagingArea()

	// The virtual genesis marker, holding the whole interval space, as reachabilityManager.Init stages it.
	root := helper.newNode(stagingArea)
	manager.stageReindexRoot(stagingArea, root)

	// A chain of 100 blocks below it. The first one is the proof-only block: it is in the
	// reachability tree but has no GHOSTDAG data. Every other block has one, with blue
	// scores counting up so the reindex window is measured the way it is on a real DAG.
	const chainLength = 100
	chain := make([]*externalapi.DomainHash, 0, chainLength)
	current := root
	for i := range chainLength {
		child := helper.newNode(stagingArea)
		helper.addChild(stagingArea, current, child, root)
		if i > 0 {
			ghostdagDataStore.stageBlueScore(child, uint64(i))
		}
		chain = append(chain, child)
		current = child
	}
	selectedTip := chain[len(chain)-1]
	ghostdagDataStore.stageBlueScore(root, 0)

	err := manager.updateReindexRoot(stagingArea, selectedTip)
	if err != nil {
		t.Fatalf("updateReindexRoot: %+v", err)
	}

	newReindexRoot, err := manager.reindexRoot(stagingArea)
	if err != nil {
		t.Fatalf("reindexRoot: %+v", err)
	}

	if newReindexRoot.Equal(root) {
		t.Fatalf("the reindex root is still the tree root %s, so it never passed the block "+
			"without GHOSTDAG data", root)
	}

	// The reindex root is expected to end up reindexWindow blocks behind the selected tip.
	expectedReindexRoot := chain[len(chain)-1-int(manager.reindexWindow)]
	if !newReindexRoot.Equal(expectedReindexRoot) {
		t.Fatalf("expected the reindex root to be %s, which is %d blocks behind the selected tip, but got %s",
			expectedReindexRoot, manager.reindexWindow, newReindexRoot)
	}
}

// TestUpdateReindexRootWhenRootIsTheSelectedTip covers the case the block processor now reaches on
// every block, rather than only when the headers selected tip changed: asking for a reindex root
// update when the root already is the selected tip. FindNextAncestor rejects an ancestor that is
// its own descendant, so without a guard this would fail block insertion.
func TestUpdateReindexRootWhenRootIsTheSelectedTip(t *testing.T) {
	reachabilityDataStore := newReachabilityDataStoreMock()
	ghostdagDataStore := newGHOSTDAGDataStoreMock()
	manager := New(nil, ghostdagDataStore, reachabilityDataStore).(*reachabilityManager)
	helper := newTestHelper(manager, t, reachabilityDataStore)

	stagingArea := model.NewStagingArea()

	root := helper.newNode(stagingArea)
	manager.stageReindexRoot(stagingArea, root)
	ghostdagDataStore.stageBlueScore(root, 1)

	if err := manager.updateReindexRoot(stagingArea, root); err != nil {
		t.Fatalf("updateReindexRoot with the root equal to the selected tip: %+v", err)
	}

	unchanged, err := manager.reindexRoot(stagingArea)
	if err != nil {
		t.Fatalf("reindexRoot: %+v", err)
	}
	if !unchanged.Equal(root) {
		t.Fatalf("expected the reindex root to stay %s, got %s", root, unchanged)
	}
}
