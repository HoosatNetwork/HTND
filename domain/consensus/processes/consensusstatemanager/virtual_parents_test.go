package consensusstatemanager_test

import (
	"sort"
	"testing"

	"github.com/Hoosat-Oy/HTND/domain/consensus"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/testapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/consensushashing"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/constants"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/testutils"
)

func TestConsensusStateManager_pickVirtualParents(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		stagingArea := model.NewStagingArea()

		tc, teardown, err := consensus.NewFactory().NewTestConsensus(consensusConfig, "TestConsensusStateManager_pickVirtualParents")
		if err != nil {
			t.Fatalf("Error setting up tc: %+v", err)
		}
		defer teardown(false)

		getSortedVirtualParents := func(tc testapi.TestConsensus) []*externalapi.DomainHash {
			virtualRelations, err := tc.BlockRelationStore().BlockRelation(tc.DatabaseContext(), stagingArea, model.VirtualBlockHash)
			if err != nil {
				t.Fatalf("Failed getting virtual block virtualRelations: %v", err)
			}

			block, err := tc.BuildBlock(&externalapi.DomainCoinbaseData{ScriptPublicKey: &externalapi.ScriptPublicKey{Script: nil, Version: 0}}, nil)
			if err != nil {
				t.Fatalf("Consensus failed building a block: %v", err)
			}
			blockParents := block.Header.DirectParents()
			sort.Sort(testutils.NewTestGhostDAGSorter(stagingArea, virtualRelations.Parents, tc, t))
			sort.Sort(testutils.NewTestGhostDAGSorter(stagingArea, blockParents, tc, t))
			if !externalapi.HashesEqual(virtualRelations.Parents, blockParents) {
				t.Fatalf("Block relations and BuildBlock return different parents for virtual, %s != %s", virtualRelations.Parents, blockParents)
			}
			return virtualRelations.Parents
		}

		// We build 3*consensusConfig.MaxBlockParents each one with blueWork higher than the other.
		parents := make([]*externalapi.DomainHash, 0, consensusConfig.MaxBlockParents[constants.GetBlockVersion()-1])
		for i := 0; i < 3*int(consensusConfig.MaxBlockParents[constants.GetBlockVersion()-1]); i++ {
			lastBlock := consensusConfig.GenesisHash
			for j := 0; j <= i; j++ {
				lastBlock, _, err = tc.AddBlock([]*externalapi.DomainHash{lastBlock}, nil, nil)
				if err != nil {
					t.Fatalf("Failed Adding block to tc: %+v", err)
				}
			}
			parents = append(parents, lastBlock)
		}

		virtualParents := getSortedVirtualParents(tc)
		// Sort parents by GHOSTDAG order
		sort.Sort(testutils.NewTestGhostDAGSorter(stagingArea, parents, tc, t))

		// Check that virtual parents are among the candidates
		maxBlockParents := int(consensusConfig.K[constants.GetBlockVersion()-1])
		if len(virtualParents) > maxBlockParents {
			t.Fatalf("Expected at most %d virtual parents, got %d", maxBlockParents, len(virtualParents))
		}
		if len(virtualParents) == 0 {
			t.Fatalf("Expected at least 1 virtual parent, got 0")
		}
		for i := 0; i < len(virtualParents); i++ {
			found := false
			for _, parent := range parents {
				if virtualParents[i].Equal(parent) {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("Virtual parent %s at position %d is not among the candidates", virtualParents[i], i)
			}
		}

		// Clear all tips.
		var virtualSelectedParent *externalapi.DomainHash
		for {
			block, err := tc.BuildBlock(&externalapi.DomainCoinbaseData{ScriptPublicKey: &externalapi.ScriptPublicKey{Script: nil, Version: 0}, ExtraData: nil}, nil)
			if err != nil {
				t.Fatalf("Failed building a block: %v", err)
			}
			err = tc.ValidateAndInsertBlock(block, true, true)
			if err != nil {
				t.Fatalf("Failed Inserting block to tc: %v", err)
			}
			virtualSelectedParent = consensushashing.BlockHash(block)
			if len(block.Header.DirectParents()) == 1 {
				break
			}
		}
		// build exactly consensusConfig.MaxBlockParents
		parents = make([]*externalapi.DomainHash, 0, consensusConfig.MaxBlockParents[constants.GetBlockVersion()-1])
		for i := 0; i < int(consensusConfig.MaxBlockParents[constants.GetBlockVersion()-1]); i++ {
			block, _, err := tc.AddBlock([]*externalapi.DomainHash{virtualSelectedParent}, nil, nil)
			if err != nil {
				t.Fatalf("Failed Adding block to tc: %+v", err)
			}
			parents = append(parents, block)
		}

		sort.Sort(testutils.NewTestGhostDAGSorter(stagingArea, parents, tc, t))
		virtualParents = getSortedVirtualParents(tc)
		// Check that all parents are virtual parents
		if len(virtualParents) < len(parents) {
			t.Fatalf("Expected at least %d virtual parents, got %d", len(parents), len(virtualParents))
		}
		for _, parent := range parents {
			found := false
			for _, vp := range virtualParents {
				if parent.Equal(vp) {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("Parent %s is not a virtual parent", parent)
			}
		}
	})
}
