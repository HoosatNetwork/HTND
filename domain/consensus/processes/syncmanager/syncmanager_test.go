package syncmanager_test

import (
	"math"
	"testing"

	"github.com/Hoosat-Oy/HTND/domain/consensus/model"

	"github.com/Hoosat-Oy/HTND/domain/consensus"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/testutils"
)

func TestSyncManager_GetHashesBetween(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		stagingArea := model.NewStagingArea()

		factory := consensus.NewFactory()
		tc, teardown, err := factory.NewTestConsensus(consensusConfig, "TestSyncManager_GetHashesBetween")
		if err != nil {
			t.Fatalf("Error setting up consensus: %+v", err)
		}
		defer teardown(false)

		// Create a DAG with the following structure:
		//          merging block
		//         /      |      \
		//      split1  split2   split3
		//        \       |      /
		//         merging block
		//         /      |      \
		//      split1  split2   split3
		//        \       |      /
		//               etc.
		allBlocks := make(map[string]bool)
		mergingBlock := consensusConfig.GenesisHash
		for i := 0; i < 10; i++ {
			splitBlocks := make([]*externalapi.DomainHash, 0, 3)
			for j := 0; j < 3; j++ {
				splitBlock, _, err := tc.AddBlock([]*externalapi.DomainHash{mergingBlock}, nil, nil)
				if err != nil {
					t.Fatalf("Failed adding block: %v", err)
				}
				splitBlocks = append(splitBlocks, splitBlock)
				allBlocks[splitBlock.String()] = true
			}

			mergingBlock, _, err = tc.AddBlock(splitBlocks, nil, nil)
			if err != nil {
				t.Fatalf("Failed adding block: %v", err)
			}
			allBlocks[mergingBlock.String()] = true
		}

		actualOrder, _, err := tc.SyncManager().GetHashesBetween(stagingArea, consensusConfig.GenesisHash, mergingBlock, math.MaxUint64)
		if err != nil {
			t.Fatalf("TestSyncManager_GetHashesBetween failed: %v", err)
		}

		// Check that all returned hashes are in allBlocks
		returnedBlocks := make(map[string]bool)
		for _, hash := range actualOrder {
			hashStr := hash.String()
			if !allBlocks[hashStr] {
				t.Fatalf("Returned hash %s is not in the expected set", hash)
			}
			if returnedBlocks[hashStr] {
				t.Fatalf("Returned hash %s appears multiple times", hash)
			}
			returnedBlocks[hashStr] = true
		}

		// Check that all expected blocks are returned
		for hashStr := range allBlocks {
			if !returnedBlocks[hashStr] {
				t.Fatalf("Expected block %s was not returned", hashStr)
			}
		}

		// Check that low=high returns empty for all blocks
		for _, hash := range actualOrder {
			empty, _, err := tc.SyncManager().GetHashesBetween(stagingArea, hash, hash, math.MaxUint64)
			if err != nil {
				t.Fatalf("GetHashesBetween failed for low=high: %v", err)
			}
			if len(empty) != 0 {
				t.Fatalf("Expected low=high to return empty, got %v", empty)
			}
		}
	})
}
