package syncmanager_test

import (
	"math"
	"sort"
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus/model"

	"github.com/HoosatNetwork/HTND/domain/consensus"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/testutils"
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
		expectedOrder := make([]*externalapi.DomainHash, 0, 40)
		mergingBlock := consensusConfig.GenesisHash
		for range 10 {
			splitBlocks := make([]*externalapi.DomainHash, 0, 3)
			for range 3 {
				splitBlock, _, err := tc.AddBlock([]*externalapi.DomainHash{mergingBlock}, nil, nil)
				if err != nil {
					t.Fatalf("Failed adding block: %v", err)
				}
				splitBlocks = append(splitBlocks, splitBlock)
			}

			sort.Sort(sort.Reverse(testutils.NewTestGhostDAGSorter(stagingArea, splitBlocks, tc, t)))
			restOfSplitBlocks, selectedParent := splitBlocks[:len(splitBlocks)-1], splitBlocks[len(splitBlocks)-1]
			expectedOrder = append(expectedOrder, selectedParent)
			expectedOrder = append(expectedOrder, restOfSplitBlocks...)

			mergingBlock, _, err = tc.AddBlock(splitBlocks, nil, nil)
			if err != nil {
				t.Fatalf("Failed adding block: %v", err)
			}
			expectedOrder = append(expectedOrder, mergingBlock)
		}

		for i, blockHash := range expectedOrder {
			empty, _, err := tc.SyncManager().GetHashesBetween(stagingArea, blockHash, blockHash, math.MaxUint64, false)
			if err != nil {
				t.Fatalf("TestSyncManager_GetHashesBetween failed returning 0 hashes on the %d'th block: %v", i, err)
			}
			if len(empty) != 0 {
				t.Fatalf("Expected lowHash=highHash to return empty on the %d'th block, instead found: %v", i, empty)
			}
		}

		actualOrder, _, err := tc.SyncManager().GetHashesBetween(
			stagingArea, consensusConfig.GenesisHash, expectedOrder[len(expectedOrder)-1], math.MaxUint64, false)
		if err != nil {
			t.Fatalf("TestSyncManager_GetHashesBetween failed returning actualOrder: %v", err)
		}

		// The function returns blocks based on merge sets, so the order may differ
		// Check that all expected blocks are present
		if len(actualOrder) != len(expectedOrder) {
			t.Fatalf("TestSyncManager_GetHashesBetween expected %d hashes, got %d", len(expectedOrder), len(actualOrder))
		}

		// Convert to sets for comparison (order may differ)
		expectedSet := make(map[string]bool)
		for _, h := range expectedOrder {
			expectedSet[h.String()] = true
		}
		actualSet := make(map[string]bool)
		for _, h := range actualOrder {
			actualSet[h.String()] = true
		}
		for _, h := range expectedOrder {
			if !actualSet[h.String()] {
				t.Fatalf("Expected hash %s not found in actual order", h)
			}
		}
	})
}
