package dagtraversalmanager_test

import (
	"math/big"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/testapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/testutils"
)

// testHash builds a distinct DomainHash from a single byte. dagtraversalmanager's other tests use
// the datastructures testutils package for this, which this file cannot import alongside
// utils/testutils, so it is inlined.
func testHash(b byte) *externalapi.DomainHash {
	var hashBytes [externalapi.DomainHashSize]byte
	hashBytes[0] = b
	return externalapi.NewDomainHashFromByteArray(&hashBytes)
}

// trustedWindowSize is the number of blocks staged as a pruned block's trusted DAA window. Any
// number above 1 works; 10 is small enough to read in a failure message.
const trustedWindowSize = 10

// stagePrunedBlockWithTrustedWindow builds the exact shape validateAndInsertBlockWithTrustedData
// leaves a pruning point in, and returns its hash.
//
// Two things define that shape, and both matter:
//   - the block's GHOSTDAG selected parent is model.VirtualGenesisBlockHash, because its real
//     selected parent was pruned away and ghostdagDataWithoutPrunedBlocks replaced it with the
//     marker;
//   - its DAA window exists ONLY as trusted data, in blocksWithTrustedDataDAAWindowStore, not as a
//     walkable chain of blocks this node holds.
func stagePrunedBlockWithTrustedWindow(t *testing.T, tc testapi.TestConsensus,
	stagingArea *model.StagingArea,
) *externalapi.DomainHash {
	t.Helper()

	prunedBlock := testHash(200)

	tc.GHOSTDAGDataStore().Stage(stagingArea, prunedBlock, externalapi.NewBlockGHOSTDAGData(
		1,                             // blueScore
		big.NewInt(1),                 // blueWork
		model.VirtualGenesisBlockHash, // selectedParent - the marker, which is the whole point
		nil, nil, nil, 0,
	), false)

	for i := range uint64(trustedWindowSize) {
		windowBlock := testHash(byte(100 + i))
		tc.BlocksWithTrustedDataDAAWindowStore().Stage(stagingArea, prunedBlock, i,
			&externalapi.BlockGHOSTDAGDataHashPair{
				Hash: windowBlock,
				GHOSTDAGData: externalapi.NewBlockGHOSTDAGData(
					i, big.NewInt(int64(i+1)), model.VirtualGenesisBlockHash, nil, nil, nil, 0),
			})
	}

	return prunedBlock
}

// TestDifficultyWindowReachesTheTrustedWindowOfAPrunedBlock is HTN-204's regression test.
//
// calculateBlockWindowHeap used to break on a virtual-genesis selected parent BEFORE the
// daaWindowStore lookup, so the walk stopped one statement short of the data it came for and a
// pruning point's window came back empty. BlockWindowHeapSlice cached that, every child built from
// its selected parent's cached slice, and a freshly synced node mined at genesis difficulty -
// 65536.01 on mainnet, about 2000x too easy - until a whole window of new blocks accumulated.
func TestDifficultyWindowReachesTheTrustedWindowOfAPrunedBlock(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		tc, tearDown, err := consensus.NewFactory().NewTestConsensus(consensusConfig,
			"TestDifficultyWindowReachesTheTrustedWindowOfAPrunedBlock")
		if err != nil {
			t.Fatalf("NewTestConsensus: %+v", err)
		}
		defer tearDown(false)

		stagingArea := model.NewStagingArea()
		prunedBlock := stagePrunedBlockWithTrustedWindow(t, tc, stagingArea)

		// true = the difficulty path.
		window, err := tc.DAGTraversalManager().BlockWindowHeapSlice(
			stagingArea, prunedBlock, trustedWindowSize*2, true)
		if err != nil {
			t.Fatalf("BlockWindowHeapSlice: %+v", err)
		}

		if len(window) != trustedWindowSize {
			t.Fatalf("expected the trusted DAA window of %d blocks, but the window has %d blocks. "+
				"An empty window here is HTN-204: every block above the pruning point inherits it and "+
				"the node mines at genesis difficulty.", trustedWindowSize, len(window))
		}
	})
}

// TestNonTrustedWindowStopsAtThePruningBoundary pins the includeTrustedWindow=false mode, which must
// keep the OLD behaviour.
//
// This was first written as "the SERVING window stops at the boundary", because the first HTN-204
// change left serving truncated. That turned out to be the wrong half to leave alone: it made the fix
// work for exactly one hop, since a node syncing from a headers-proof peer received that peer's
// truncated window. Serving now uses the trusted window too - see DAABlockWindow and
// consensus.trustedWindowGHOSTDAGData - and the end-to-end proof lives in blockprocessor's
// htn204_difficulty_sync_test.go.
//
// What still uses false, and why it must: pastMedianTimeManager, which feeds validateMedianTime - an
// ENABLED check, so widening its window would change which blocks this node accepts - and
// pruningManager.blocksToKeep.
func TestNonTrustedWindowStopsAtThePruningBoundary(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		tc, tearDown, err := consensus.NewFactory().NewTestConsensus(consensusConfig,
			"TestNonTrustedWindowStopsAtThePruningBoundary")
		if err != nil {
			t.Fatalf("NewTestConsensus: %+v", err)
		}
		defer tearDown(false)

		stagingArea := model.NewStagingArea()
		prunedBlock := stagePrunedBlockWithTrustedWindow(t, tc, stagingArea)

		// false = past median time and the pruning manager's blocksToKeep.
		window, err := tc.DAGTraversalManager().BlockWindowHeapSlice(
			stagingArea, prunedBlock, trustedWindowSize*2, false)
		if err != nil {
			t.Fatalf("BlockWindowHeapSlice: %+v", err)
		}

		if len(window) != 0 {
			t.Fatalf("the non-trusted window walked into the trusted window and returned %d blocks. "+
				"Past median time reads this window, and validateMedianTime is enabled, so widening it "+
				"changes which blocks this node accepts.", len(window))
		}
	})
}

// TestTheTwoWindowsDoNotShareACacheEntry pins the containment. Both windows are asked for, for the
// same block and the same size, in both orders - and each must still get its own answer.
//
// The slice cache was keyed by (blockHash, windowSize) alone. Once the two paths can return
// different results for that key, a shared entry means whichever ran first silently decides what the
// other sees: either the difficulty path gets an empty window and the node mines at genesis
// difficulty anyway, or the serving path gets the trusted window and kills a peer's IBD. Neither
// failure would look like a cache bug.
func TestTheTwoWindowsDoNotShareACacheEntry(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		for _, testCase := range []struct {
			name         string
			firstTrusted bool
		}{
			{"difficulty first", true},
			{"serving first", false},
		} {
			t.Run(testCase.name, func(t *testing.T) {
				tc, tearDown, err := consensus.NewFactory().NewTestConsensus(consensusConfig,
					"TestTheTwoWindowsDoNotShareACacheEntry"+testCase.name)
				if err != nil {
					t.Fatalf("NewTestConsensus: %+v", err)
				}
				defer tearDown(false)

				stagingArea := model.NewStagingArea()
				prunedBlock := stagePrunedBlockWithTrustedWindow(t, tc, stagingArea)

				// Warm the cache with whichever mode goes first.
				if _, err := tc.DAGTraversalManager().BlockWindowHeapSlice(
					stagingArea, prunedBlock, trustedWindowSize*2, testCase.firstTrusted); err != nil {
					t.Fatalf("first BlockWindowHeapSlice: %+v", err)
				}

				difficultyWindow, err := tc.DAGTraversalManager().BlockWindowHeapSlice(
					stagingArea, prunedBlock, trustedWindowSize*2, true)
				if err != nil {
					t.Fatalf("difficulty BlockWindowHeapSlice: %+v", err)
				}
				servingWindow, err := tc.DAGTraversalManager().BlockWindowHeapSlice(
					stagingArea, prunedBlock, trustedWindowSize*2, false)
				if err != nil {
					t.Fatalf("serving BlockWindowHeapSlice: %+v", err)
				}

				if len(difficultyWindow) != trustedWindowSize {
					t.Errorf("difficulty window has %d blocks, want %d - the serving path's cached "+
						"answer leaked into it", len(difficultyWindow), trustedWindowSize)
				}
				if len(servingWindow) != 0 {
					t.Errorf("non-trusted window has %d blocks, want 0 - the trusted window's cached "+
						"answer leaked into it", len(servingWindow))
				}
			})
		}
	})
}
