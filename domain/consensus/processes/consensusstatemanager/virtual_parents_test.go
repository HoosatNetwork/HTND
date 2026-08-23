package consensusstatemanager_test

import (
	"slices"
	"sort"
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus"
	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/testapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/testutils"
)

func TestConsensusStateManager_pickVirtualParents(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		stagingArea := model.NewStagingArea()

		tc, teardown, err := consensus.NewFactory().NewTestConsensus(consensusConfig, "TestConsensusStateManager_pickVirtualParents")
		if err != nil {
			t.Fatalf("Error setting up tc: %+v", err)
		}
		defer teardown(false)

		// This test mines many chains and sibling blocks from the same
		// ancestors, so blocks would otherwise share both blue score and
		// default coinbase data and collide on their coinbase transaction ID.
		tc.BlockBuilder().EnableUniqueDefaultCoinbaseExtraData()

		getSortedVirtualParents := func(tc testapi.TestConsensus) ([]*externalapi.DomainHash, int) {
			virtualRelations, err := tc.BlockRelationStore().BlockRelation(tc.DatabaseContext(), stagingArea, model.VirtualBlockHash)
			if err != nil {
				t.Fatalf("Failed getting virtual block virtualRelations: %v", err)
			}

			block, err := tc.BuildBlock(&externalapi.DomainCoinbaseData{ScriptPublicKey: &externalapi.ScriptPublicKey{Script: nil, Version: 0}}, nil)
			if err != nil {
				t.Fatalf("Consensus failed building a block: %v", err)
			}
			blockVersion := int(block.Header.Version())
			if blockVersion <= 0 || blockVersion > len(consensusConfig.MaxBlockParents) {
				t.Fatalf("Unexpected block version %d (MaxBlockParents has %d entries)", blockVersion, len(consensusConfig.MaxBlockParents))
			}
			maxParents := int(consensusConfig.MaxBlockParents[blockVersion-1])
			if maxParents <= 0 {
				t.Fatalf("Unexpected MaxBlockParents[%d]=%d", blockVersion-1, maxParents)
			}
			blockParents := block.Header.DirectParents()
			sort.Sort(testutils.NewTestGhostDAGSorter(stagingArea, virtualRelations.Parents, tc, t))
			sort.Sort(testutils.NewTestGhostDAGSorter(stagingArea, blockParents, tc, t))
			if !externalapi.HashesEqual(virtualRelations.Parents, blockParents) {
				t.Fatalf("Block relations and BuildBlock return different parents for virtual, %s != %s", virtualRelations.Parents, blockParents)
			}
			return virtualRelations.Parents, maxParents
		}

		_, maxParents := getSortedVirtualParents(tc)

		// We build 3*maxParents chains, each one with blueWork higher than the other.
		for i := 0; i < 3*maxParents; i++ {
			lastBlock := consensusConfig.GenesisHash
			for j := 0; j <= i; j++ {
				lastBlock, _, err = tc.AddBlock([]*externalapi.DomainHash{lastBlock}, nil, nil)
				if err != nil {
					t.Fatalf("Failed Adding block to tc: %+v", err)
				}
			}
		}

		virtualParents, maxParents := getSortedVirtualParents(tc)
		if len(virtualParents) > maxParents {
			t.Fatalf("Expected at most %d virtual parents, got %d", maxParents, len(virtualParents))
		}

		// Clear all tips.
		var virtualSelectedParent *externalapi.DomainHash
		for {
			virtualParents, _ := getSortedVirtualParents(tc)
			if len(virtualParents) == 1 {
				virtualSelectedParent = virtualParents[0]
				break
			}
			_, _, err := tc.AddBlock(virtualParents, nil, nil)
			if err != nil {
				t.Fatalf("Failed Adding block to tc: %+v", err)
			}
		}
		// build exactly consensusConfig.MaxBlockParents
		_, maxParents = getSortedVirtualParents(tc)
		parents := make([]*externalapi.DomainHash, 0, maxParents)
		for i := 0; i < maxParents; i++ {
			block, _, err := tc.AddBlock([]*externalapi.DomainHash{virtualSelectedParent}, nil, nil)
			if err != nil {
				t.Fatalf("Failed Adding block to tc: %+v", err)
			}
			parents = append(parents, block)
		}

		sort.Sort(testutils.NewTestGhostDAGSorter(stagingArea, parents, tc, t))
		virtualParents, _ = getSortedVirtualParents(tc)
		// Check that all parents are virtual parents
		if len(virtualParents) < len(parents) {
			t.Fatalf("Expected at least %d virtual parents, got %d", len(parents), len(virtualParents))
		}
		for _, parent := range parents {
			found := slices.ContainsFunc(virtualParents, parent.Equal)
			if !found {
				t.Fatalf("Parent %s is not a virtual parent", parent)
			}
		}
	})
}
