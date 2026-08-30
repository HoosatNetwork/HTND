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

// TestSyncManager_GetHashesBetweenIsTopological asserts the property a syncing peer actually
// depends on: every returned block appears after all of its parents that are also in the batch.
//
// The peer inserts headers in the order it receives them and rejects any block whose parents it
// hasn't seen yet with ErrMissingParents, aborting the whole IBD. A previous implementation ordered
// the batch by blue score, which counts blue blocks rather than accumulating work and so is not a
// topological key - a parent on a low-difficulty branch can outscore the child that merges it. That
// only misorders a batch when such a pair happens to land in it, which is why it failed on some
// nodes and not others. Checking set membership alone (as TestSyncManager_GetHashesBetween does)
// cannot catch it.
func TestSyncManager_GetHashesBetweenIsTopological(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		stagingArea := model.NewStagingArea()

		factory := consensus.NewFactory()
		tc, teardown, err := factory.NewTestConsensus(consensusConfig, "TestSyncManager_GetHashesBetweenIsTopological")
		if err != nil {
			t.Fatalf("Error setting up consensus: %+v", err)
		}
		defer teardown(false)

		// Build a DAG of repeated 3-way splits joined by merging blocks, so the batch contains real
		// merge sets with multiple parents rather than a single chain.
		tip := consensusConfig.GenesisHash
		for range 10 {
			splitBlocks := make([]*externalapi.DomainHash, 0, 3)
			for range 3 {
				splitBlock, _, err := tc.AddBlock([]*externalapi.DomainHash{tip}, nil, nil)
				if err != nil {
					t.Fatalf("Failed adding block: %v", err)
				}
				splitBlocks = append(splitBlocks, splitBlock)
			}
			tip, _, err = tc.AddBlock(splitBlocks, nil, nil)
			if err != nil {
				t.Fatalf("Failed adding merging block: %v", err)
			}
		}

		for _, brute := range []bool{false, true} {
			hashes, _, err := tc.SyncManager().GetHashesBetween(
				stagingArea, consensusConfig.GenesisHash, tip, math.MaxUint64, brute)
			if err != nil {
				t.Fatalf("GetHashesBetween(brute=%t) failed: %v", brute, err)
			}
			if len(hashes) == 0 {
				t.Fatalf("GetHashesBetween(brute=%t) returned no hashes", brute)
			}

			positionOf := make(map[externalapi.DomainHash]int, len(hashes))
			for i, hash := range hashes {
				if previous, isDuplicate := positionOf[*hash]; isDuplicate {
					t.Fatalf("GetHashesBetween(brute=%t) returned %s twice, at %d and %d",
						brute, hash, previous, i)
				}
				positionOf[*hash] = i
			}

			for i, hash := range hashes {
				header, err := tc.GetBlockHeader(hash)
				if err != nil {
					t.Fatalf("Failed getting header for %s: %v", hash, err)
				}
				for _, parent := range header.DirectParents() {
					// A parent outside the batch is fine - it's below lowHash, so the peer
					// already has it. A parent inside the batch must come first.
					parentPosition, isInBatch := positionOf[*parent]
					if isInBatch && parentPosition > i {
						t.Fatalf("GetHashesBetween(brute=%t) is not in topological order: block %s at "+
							"index %d comes before its parent %s at index %d, so a syncing peer would "+
							"reject it with ErrMissingParents", brute, hash, i, parent, parentPosition)
					}
				}
			}
		}
	})
}
