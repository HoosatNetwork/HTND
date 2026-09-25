package consensusstatemanager_test

import (
	"math/rand"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/testutils"
)

// TestResolveVirtualRepeatedlySkipsAKnownFinalityViolatingTip is the regression test for the
// findNextPendingTip fast path added on 2026-09-19: during the mainnet backlog-catchup incident,
// ResolveVirtual ran hundreds of small chunks back to back, and each one called findNextPendingTip,
// which re-lists every current DAG tip and re-runs isViolatingFinality on all of them from scratch -
// including tips it had already confirmed violate finality on a previous chunk. That check is
// monotonic (the finality/pruning point it compares against only ever moves forward), so re-checking
// an already-confirmed-violating tip can never change the answer; it only wastes time under the
// consensus lock that mining and IBD are also contending for.
//
// This builds the same finality-violation attack shape as TestFinalityResolveVirtual (a heavier side
// chain that arrives after the main chain has already advanced finality past their common ancestor),
// but resolves it over several small ResolveVirtualWithMaxParam chunks instead of one large call, so
// findNextPendingTip is invoked against the violating tip more than once - exactly the pattern that
// wasted work in production. It pins the correctness side of the fix: virtual must stay off the
// violating side chain on every single chunk, not just the first one where the violation is freshly
// discovered and cached.
func TestResolveVirtualRepeatedlySkipsAKnownFinalityViolatingTip(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		// Set finalityInterval to 20 blocks, so that test runs quickly
		consensusConfig.FinalityDuration = []time.Duration{20 * consensusConfig.TargetTimePerBlock[constants.GetBlockVersion()-1]}

		factory := consensus.NewFactory()
		tc, teardown, err := factory.NewTestConsensus(consensusConfig, "TestResolveVirtualRepeatedlySkipsAKnownFinalityViolatingTip")
		if err != nil {
			t.Fatalf("Error setting up consensus: %+v", err)
		}
		defer teardown(false)

		tip := consensusConfig.GenesisHash
		for {
			tip, _, err = tc.AddBlock([]*externalapi.DomainHash{tip}, nil, nil)
			if err != nil {
				t.Fatalf("AddBlock: %+v", err)
			}

			virtualFinalityPoint, err := tc.FinalityManager().VirtualFinalityPoint(model.NewStagingArea())
			if err != nil {
				t.Fatalf("VirtualFinalityPoint: %+v", err)
			}

			if !virtualFinalityPoint.Equal(consensusConfig.GenesisHash) {
				break
			}
		}

		virtualSelectedParent, err := tc.GetVirtualSelectedParent()
		if err != nil {
			t.Fatalf("GetVirtualSelectedParent: %+v", err)
		}

		stagingArea := model.NewStagingArea()
		virtualSelectedParentGHOSTDAGData, err := tc.GHOSTDAGDataStore().Get(tc.DatabaseContext(), stagingArea, virtualSelectedParent, false)
		if err != nil {
			t.Fatalf("GHOSTDAGDataStore.Get: %+v", err)
		}

		// Build a heavier side chain from genesis, on a separate consensus, so it never sees the main
		// chain's finality advance - matching TestFinalityResolveVirtual's attacker setup.
		tcAttacker, teardownAttacker, err := factory.NewTestConsensus(consensusConfig, "TestResolveVirtualRepeatedlySkipsAKnownFinalityViolatingTip_attacker")
		if err != nil {
			t.Fatalf("Error setting up attacker consensus: %+v", err)
		}
		defer teardownAttacker(false)

		var sideChain []*externalapi.DomainBlock
		for i := uint64(0); ; i++ {
			tips, err := tcAttacker.Tips()
			if err != nil {
				t.Fatalf("Tips: %+v", err)
			}

			block, _, err := tcAttacker.BuildBlockWithParents(tips, nil, nil)
			if err != nil {
				t.Fatalf("BuildBlockWithParents: %+v", err)
			}

			if i == 0 {
				mutableHeader := block.Header.ToMutable()
				// #nosec G404 -- deterministic-enough nonce perturbation for a unique test side chain.
				mutableHeader.SetNonce(uint64(rand.NewSource(84147).Int63()))
				block.Header = mutableHeader.ToImmutable()
			}

			err = tcAttacker.ValidateAndInsertBlock(block, true, true)
			if err != nil {
				t.Fatalf("ValidateAndInsertBlock: %+v", err)
			}

			sideChain = append(sideChain, block)

			blockHash := consensushashing.BlockHash(block)
			ghostdagData, err := tcAttacker.GHOSTDAGDataStore().Get(tcAttacker.DatabaseContext(), stagingArea, blockHash, false)
			if err != nil {
				t.Fatalf("GHOSTDAGDataStore.Get: %+v", err)
			}

			if virtualSelectedParentGHOSTDAGData.BlueWork().Cmp(ghostdagData.BlueWork()) == -1 {
				break
			}
		}

		// Insert the side chain into the main consensus without letting it auto-update virtual, so it
		// stays pending and has to be picked up by explicit ResolveVirtual chunks below - the same shape
		// as a node that fell behind and is now catching up a large backlog.
		for _, block := range sideChain {
			err := tc.ValidateAndInsertBlock(block, false, true)
			if err != nil {
				t.Fatalf("ValidateAndInsertBlock: %+v", err)
			}
		}

		// Resolve over several small chunks rather than one big call, so findNextPendingTip runs against
		// the same already-finality-violating side chain tip more than once: first discovering and
		// caching the violation, then hitting the cache on every subsequent chunk.
		for i := 0; i < len(sideChain)+2; i++ {
			_, _, err = tc.ResolveVirtualWithMaxParam(1)
			if err != nil {
				t.Fatalf("ResolveVirtualWithMaxParam chunk %d: %+v", i, err)
			}

			newVirtualSelectedParent, err := tc.GetVirtualSelectedParent()
			if err != nil {
				t.Fatalf("GetVirtualSelectedParent chunk %d: %+v", i, err)
			}
			if !newVirtualSelectedParent.Equal(virtualSelectedParent) {
				t.Fatalf("chunk %d: virtual reorged onto the finality-violating side chain (selected parent "+
					"is now %s, expected it to stay %s)", i, newVirtualSelectedParent, virtualSelectedParent)
			}
		}
	})
}
