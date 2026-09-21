package dagtraversalmanager_test

import (
	"math/big"
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus"
	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/testapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/testutils"
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

// TestServingWindowStopsAtThePruningBoundary is the other half, and the reason the original fix was
// reverted. It must keep the OLD behaviour.
//
// The serving path answers a peer's pruning-point-anticone request. If it names the blocks in a
// pruned block's trusted window, the serving node is then asked for TrustedDataDataDAAHeader for
// each of them - and it has neither their GHOSTDAG data nor their trusted-window entries, so it
// returns not-found and the peer's IBD dies. Reproduced 3/3 for docs/design/HTN-204.md.
func TestServingWindowStopsAtThePruningBoundary(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		tc, tearDown, err := consensus.NewFactory().NewTestConsensus(consensusConfig,
			"TestServingWindowStopsAtThePruningBoundary")
		if err != nil {
			t.Fatalf("NewTestConsensus: %+v", err)
		}
		defer tearDown(false)

		stagingArea := model.NewStagingArea()
		prunedBlock := stagePrunedBlockWithTrustedWindow(t, tc, stagingArea)

		// false = the serving path, and everything else that must not change: past median time and
		// the pruning manager's blocksToKeep.
		window, err := tc.DAGTraversalManager().BlockWindowHeapSlice(
			stagingArea, prunedBlock, trustedWindowSize*2, false)
		if err != nil {
			t.Fatalf("BlockWindowHeapSlice: %+v", err)
		}

		if len(window) != 0 {
			t.Fatalf("the serving window walked into the trusted window and returned %d blocks. "+
				"Serving these to a peer kills its IBD, because this node cannot answer "+
				"TrustedDataDataDAAHeader for any of them.", len(window))
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
					t.Errorf("serving window has %d blocks, want 0 - the difficulty path's cached "+
						"answer leaked into it", len(servingWindow))
				}
			})
		}
	})
}
