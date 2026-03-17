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

		// We build 2*consensusConfig.MaxBlockParents each one with blueWork higher than the other.
		parents := make([]*externalapi.DomainHash, 0, consensusConfig.MaxBlockParents[constants.GetBlockVersion()-1])
		for i := 0; i < 2*int(consensusConfig.MaxBlockParents[constants.GetBlockVersion()-1]); i++ {
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
		sort.Sort(testutils.NewTestGhostDAGSorter(stagingArea, parents, tc, t))

		candidateParentSet := make(map[externalapi.DomainHash]struct{}, len(parents))
		for _, parent := range parents {
			candidateParentSet[*parent] = struct{}{}
		}

		if len(virtualParents) > int(consensusConfig.MaxBlockParents[constants.GetBlockVersion()-1]) {
			t.Fatalf("Expected at most %d virtual parents, got %d", consensusConfig.MaxBlockParents[constants.GetBlockVersion()-1], len(virtualParents))
		}
		for i, virtualParent := range virtualParents {
			if _, ok := candidateParentSet[*virtualParent]; !ok {
				t.Fatalf("Unexpected virtual parent at %d: %s", i, virtualParent)
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
		if !externalapi.HashesEqual(virtualParents, parents) {
			t.Fatalf("Expected VirtualParents and parents to be equal, instead: %s != %s", virtualParents, parents)
		}
	})
}
