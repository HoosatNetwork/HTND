package ghostdagmanager_test

import (
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus"
	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/testutils"
)

// TestDynamicKIsNeverPersistedAsZero pins the invariant that a block's stored GHOSTDAG data always
// records the K its coloring actually used.
//
// GHOSTDAG assigned newBlockData.dynamicK on exactly one of its four paths (the one that calls
// CalculateRank). The other three - a cached K, the static K below block version 6, and genesis -
// colored the block with a perfectly good k and then wrote the zero value back to the store.
//
// For a real block that was merely lossy, because its parents are fixed and nothing recolors it.
// For virtual it was fatal. Virtual is recolored on every update and always has stored data by then,
// so it took the cached-K path, wrote dynamicK=0 over the correct value, and every call afterwards
// read that 0 back as its K. checkBlueCandidate returns early on
// `len(mergeSetBlues) == k+1`, so with k=0 it rejects the very first candidate and nothing but the
// selected parent can ever be blue again.
//
// The visible damage was in the bounded merge depth rule rather than in the coloring, via the blue
// score rather than via the colors, which is why it was so hard to see. Virtual's blue score is
// understated by exactly the blues it failed to count, so its requiredBlueScore - and the merge
// depth root boundedMergeBreakingParents judges candidate parents against - sits one chain block
// older than the root a block built on those same parents is validated against. Any branch forking
// in the gap between the two roots is approved by the filter and rejected by the validator, and the
// node mines blocks it then rejects itself, permanently, with no way out: virtual is only recolored
// when a block arrives, and no block can be accepted.
//
// Observed on mainnet 2026-09-04: virtual 1 blue -> blueScore 210687165 -> root at 210683560, while
// the block it produced had 5 blues -> blueScore 210687169 -> root at 210683565. All 175 reds sat in
// that five-blue-score gap.
func TestDynamicKIsNeverPersistedAsZero(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		factory := consensus.NewFactory()
		tc, teardown, err := factory.NewTestConsensus(consensusConfig, "TestDynamicKIsNeverPersistedAsZero")
		if err != nil {
			t.Fatalf("NewTestConsensus: %+v", err)
		}
		defer teardown(false)

		assertDynamicK := func(when string, blockHash *externalapi.DomainHash) {
			t.Helper()
			stagingArea := model.NewStagingArea()
			ghostdagData, err := tc.GHOSTDAGDataStore().Get(tc.DatabaseContext(), stagingArea, blockHash, false)
			if err != nil {
				t.Fatalf("%s: GHOSTDAGDataStore.Get(%s): %+v", when, blockHash, err)
			}
			if ghostdagData.DynamicK() == 0 {
				t.Fatalf("%s: %s was stored with dynamicK=0. Whatever K the coloring used, it must be "+
					"recorded - a stored 0 is read back as the K for the next coloring, and k=0 lets "+
					"nothing but the selected parent be blue.", when, blockHash)
			}
		}

		// Build a small DAG with real width, so virtual is recolored repeatedly and, on the buggy code,
		// takes the cached-K path from the second update onwards.
		tips := []*externalapi.DomainHash{consensusConfig.GenesisHash}
		for i := range 4 {
			var newTips []*externalapi.DomainHash
			for range 2 {
				blockHash, _, err := tc.AddBlock(tips, nil, nil)
				if err != nil {
					t.Fatalf("AddBlock at depth %d: %+v", i, err)
				}
				newTips = append(newTips, blockHash)
				assertDynamicK("after adding a block", blockHash)
				assertDynamicK("after adding a block", model.VirtualBlockHash)
			}
			tips = newTips
		}

		// The point of the invariant: virtual merging several parents must still be able to color more
		// than just its selected parent blue. With a K of 0 this is exactly 1.
		stagingArea := model.NewStagingArea()
		virtualGHOSTDAGData, err := tc.GHOSTDAGDataStore().Get(tc.DatabaseContext(), stagingArea, model.VirtualBlockHash, false)
		if err != nil {
			t.Fatalf("GHOSTDAGDataStore.Get(virtual): %+v", err)
		}
		virtualParents, err := tc.DAGTopologyManager().Parents(stagingArea, model.VirtualBlockHash)
		if err != nil {
			t.Fatalf("Parents(virtual): %+v", err)
		}
		if len(virtualParents) > 1 && len(virtualGHOSTDAGData.MergeSetBlues()) == 1 {
			t.Fatalf("virtual has %d parents but only its selected parent is blue - this is the k=0 "+
				"collapse: blueScore advances by 1 per update however wide the DAG is, and bounded "+
				"merge depth loses every kosherizing block", len(virtualParents))
		}
	})
}
