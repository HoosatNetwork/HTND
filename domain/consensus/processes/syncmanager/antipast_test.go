package syncmanager_test

import (
	"math"
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus"
	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/testutils"
)

// TestAntiPastHashesBetween_BasicChain tests the basic functionality with a simple chain
func TestAntiPastHashesBetween_BasicChain(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		stagingArea := model.NewStagingArea()

		factory := consensus.NewFactory()
		tc, teardown, err := factory.NewTestConsensus(consensusConfig, "TestAntiPastHashesBetween_BasicChain")
		if err != nil {
			t.Fatalf("Error setting up consensus: %+v", err)
		}
		defer teardown(false)

		chain := []*externalapi.DomainHash{consensusConfig.GenesisHash}
		tipHash := consensusConfig.GenesisHash
		for i := 0; i < 10; i++ {
			tipHash, _, err = tc.AddBlock([]*externalapi.DomainHash{tipHash}, nil, nil)
			if err != nil {
				t.Fatalf("Failed adding block: %v", err)
			}
			chain = append(chain, tipHash)
		}

		hashes, actualHighHash, err := tc.SyncManager().GetHashesBetween(stagingArea, chain[0], chain[10], math.MaxUint64, false)
		if err != nil {
			t.Fatalf("GetHashesBetween failed: %v", err)
		}

		// The function returns blocks based on merge sets, not necessarily in chain order
		// Verify all expected blocks are present (order may differ)
		expected := chain[1:11]
		if len(hashes) != len(expected) {
			t.Fatalf("Expected %d hashes, got %d\nExpected: %v\nActual: %v", len(expected), len(hashes), expected, hashes)
		}

		// Convert to sets for comparison
		expectedSet := make(map[string]bool)
		for _, h := range expected {
			expectedSet[h.String()] = true
		}
		actualSet := make(map[string]bool)
		for _, h := range hashes {
			actualSet[h.String()] = true
		}
		for _, h := range expected {
			if !actualSet[h.String()] {
				t.Fatalf("Expected hash %s not found in result", h)
			}
		}

		if !actualHighHash.Equal(chain[10]) {
			t.Fatalf("Expected actualHighHash: %s\nActual: %s", chain[10], actualHighHash)
		}
	})
}

// TestAntiPastHashesBetween_SameHash tests when lowHash equals highHash
func TestAntiPastHashesBetween_SameHash(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		stagingArea := model.NewStagingArea()

		factory := consensus.NewFactory()
		tc, teardown, err := factory.NewTestConsensus(consensusConfig, "TestAntiPastHashesBetween_SameHash")
		if err != nil {
			t.Fatalf("Error setting up consensus: %+v", err)
		}
		defer teardown(false)

		hashes, actualHighHash, err := tc.SyncManager().GetHashesBetween(stagingArea, consensusConfig.GenesisHash, consensusConfig.GenesisHash, math.MaxUint64, false)
		if err != nil {
			t.Fatalf("GetHashesBetween failed: %v", err)
		}

		if len(hashes) != 0 {
			t.Fatalf("Expected empty hashes for same hash, got: %v", hashes)
		}

		if !actualHighHash.Equal(consensusConfig.GenesisHash) {
			t.Fatalf("Expected actualHighHash to be genesis hash for same hash, got: %s", actualHighHash)
		}
	})
}

// TestAntiPastHashesBetween_ComplexDAGWithMerge tests DAG with merge and verifies all parents are included
func TestAntiPastHashesBetween_ComplexDAGWithMerge(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		stagingArea := model.NewStagingArea()

		factory := consensus.NewFactory()
		tc, teardown, err := factory.NewTestConsensus(consensusConfig, "TestAntiPastHashesBetween_ComplexDAGWithMerge")
		if err != nil {
			t.Fatalf("Error setting up consensus: %+v", err)
		}
		defer teardown(false)

		// Create DAG:
		// Genesis -> A -> B -> C
		//          \-> D -> E
		//                \-> F (parents: E, C)
		//                      \-> G

		genesis := consensusConfig.GenesisHash

		a, _, err := tc.AddBlock([]*externalapi.DomainHash{genesis}, nil, nil)
		if err != nil {
			t.Fatalf("Failed adding block A: %v", err)
		}
		b, _, err := tc.AddBlock([]*externalapi.DomainHash{a}, nil, nil)
		if err != nil {
			t.Fatalf("Failed adding block B: %v", err)
		}
		c, _, err := tc.AddBlock([]*externalapi.DomainHash{b}, nil, nil)
		if err != nil {
			t.Fatalf("Failed adding block C: %v", err)
		}

		d, _, err := tc.AddBlock([]*externalapi.DomainHash{genesis}, nil, nil)
		if err != nil {
			t.Fatalf("Failed adding block D: %v", err)
		}
		e, _, err := tc.AddBlock([]*externalapi.DomainHash{d}, nil, nil)
		if err != nil {
			t.Fatalf("Failed adding block E: %v", err)
		}

		// F has two parents: E and C (merge point)
		f, _, err := tc.AddBlock([]*externalapi.DomainHash{e, c}, nil, nil)
		if err != nil {
			t.Fatalf("Failed adding block F: %v", err)
		}

		g, _, err := tc.AddBlock([]*externalapi.DomainHash{f}, nil, nil)
		if err != nil {
			t.Fatalf("Failed adding block G: %v", err)
		}

		// Test: Get hashes from E to G
		// F is on the path and has parent C which is NOT in E's selected parent chain
		// This is the critical case - C must be included or we get missing parents

		hashes, actualHighHash, err := tc.SyncManager().GetHashesBetween(stagingArea, e, g, math.MaxUint64, false)
		if err != nil {
			t.Fatalf("GetHashesBetween failed: %v", err)
		}
		if !actualHighHash.Equal(g) {
			t.Fatalf("Expected actualHighHash: %s, got: %s", g, actualHighHash)
		}

		t.Logf("Hashes between E and G: %v", hashes)

		// Check that for every block in the result, all its parents are either:
		// 1. In the result
		// 2. The lowHash (E)
		// 3. In the past of lowHash (E)
		// If not, we have a missing parents bug that will cause ErrMissingParents during IBD

		blockSet := make(map[string]bool)
		for _, hash := range hashes {
			blockSet[hash.String()] = true
		}

		for _, blockHash := range hashes {
			header, err := tc.BlockHeaderStore().BlockHeader(tc.DatabaseContext(), stagingArea, blockHash)
			if err != nil {
				t.Fatalf("Failed to get header for %s: %v", blockHash, err)
			}

			t.Logf("Block %s has parents %v", blockHash, header.Parents())

			for _, parentLevel := range header.Parents() {
				for _, parent := range parentLevel {
					if blockSet[parent.String()] {
						continue
					}
					if parent.Equal(e) {
						continue
					}
					isInPastOfE, err := tc.DAGTopologyManager().IsAncestorOf(stagingArea, parent, e)
					if err != nil {
						t.Fatalf("Failed to check if %s is in past of %s: %v", parent, e, err)
					}
					if isInPastOfE {
						continue
					}
					// CRITICAL BUG: Parent is missing and not in past of lowHash
					t.Errorf("BUG FOUND: Block %s has parent %s NOT in result and NOT in past of %s! "+
						"This causes ErrMissingParents during IBD when peer receives header without its parent.",
						blockHash, parent, e)
				}
			}
		}
	})
}

// TestAntiPastHashesBetween_MissingParentInMergeSet tests the specific case where
// a block's merge set contains a parent that gets filtered out
func TestAntiPastHashesBetween_MissingParentInMergeSet(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		stagingArea := model.NewStagingArea()

		factory := consensus.NewFactory()
		tc, teardown, err := factory.NewTestConsensus(consensusConfig, "TestAntiPastHashesBetween_MissingParentInMergeSet")
		if err != nil {
			t.Fatalf("Error setting up consensus: %+v", err)
		}
		defer teardown(false)

		// Simulate the real-world IBD scenario from the log:
		// Block with hash 3ba7ac9a1d0f8262ba05e3e5c00fab85ffc9ce9ed8ddfaec6709615af7a6c531
		// has parents that are missing
		//
		// Create: genesis -> A -> B -> C (selected chain)
		//              \-> D -> E -> F
		//                        \-> G (parents: F, C)
		//                              \-> H

		genesis := consensusConfig.GenesisHash

		a, _, err := tc.AddBlock([]*externalapi.DomainHash{genesis}, nil, nil)
		if err != nil {
			t.Fatalf("Failed adding block A: %v", err)
		}
		b, _, err := tc.AddBlock([]*externalapi.DomainHash{a}, nil, nil)
		if err != nil {
			t.Fatalf("Failed adding block B: %v", err)
		}
		c, _, err := tc.AddBlock([]*externalapi.DomainHash{b}, nil, nil)
		if err != nil {
			t.Fatalf("Failed adding block C: %v", err)
		}

		d, _, err := tc.AddBlock([]*externalapi.DomainHash{genesis}, nil, nil)
		if err != nil {
			t.Fatalf("Failed adding block D: %v", err)
		}
		e, _, err := tc.AddBlock([]*externalapi.DomainHash{d}, nil, nil)
		if err != nil {
			t.Fatalf("Failed adding block E: %v", err)
		}
		f, _, err := tc.AddBlock([]*externalapi.DomainHash{e}, nil, nil)
		if err != nil {
			t.Fatalf("Failed adding block F: %v", err)
		}

		// G has parents F and C - this is the merge point
		g, _, err := tc.AddBlock([]*externalapi.DomainHash{f, c}, nil, nil)
		if err != nil {
			t.Fatalf("Failed adding block G: %v", err)
		}

		h, _, err := tc.AddBlock([]*externalapi.DomainHash{g}, nil, nil)
		if err != nil {
			t.Fatalf("Failed adding block H: %v", err)
		}

		// Scenario 1: Get hashes from D to H
		// This should include F and G, and G has parent C
		// If C is not in D's past, it should be included

		hashes, _, err := tc.SyncManager().GetHashesBetween(stagingArea, d, h, math.MaxUint64, false)
		if err != nil {
			t.Fatalf("GetHashesBetween (D to H) failed: %v", err)
		}

		t.Logf("Hashes between D and H: %v", hashes)

		blockSet := make(map[string]bool)
		for _, hash := range hashes {
			blockSet[hash.String()] = true
		}

		// Check all blocks in result have their parents
		for _, blockHash := range hashes {
			header, err := tc.BlockHeaderStore().BlockHeader(tc.DatabaseContext(), stagingArea, blockHash)
			if err != nil {
				t.Fatalf("Failed to get header: %v", err)
			}

			for _, parentLevel := range header.Parents() {
				for _, parent := range parentLevel {
					if blockSet[parent.String()] || parent.Equal(d) {
						continue
					}
					isInPastOfD, err := tc.DAGTopologyManager().IsAncestorOf(stagingArea, parent, d)
					if err != nil {
						t.Fatalf("Failed IsAncestorOf: %v", err)
					}
					if isInPastOfD {
						continue
					}
					t.Errorf("Missing parent %s for block %s when lowHash=%s", parent, blockHash, d)
				}
			}
		}

		// Scenario 2: Get hashes from E to H (E is on the other branch)
		// This is more likely to expose the bug
		hashes2, _, err := tc.SyncManager().GetHashesBetween(stagingArea, e, h, math.MaxUint64, false)
		if err != nil {
			t.Fatalf("GetHashesBetween (E to H) failed: %v", err)
		}

		t.Logf("Hashes between E and H: %v", hashes2)

		blockSet2 := make(map[string]bool)
		for _, hash := range hashes2 {
			blockSet2[hash.String()] = true
		}

		for _, blockHash := range hashes2 {
			header, err := tc.BlockHeaderStore().BlockHeader(tc.DatabaseContext(), stagingArea, blockHash)
			if err != nil {
				t.Fatalf("Failed to get header: %v", err)
			}

			for _, parentLevel := range header.Parents() {
				for _, parent := range parentLevel {
					if blockSet2[parent.String()] || parent.Equal(e) {
						continue
					}
					isInPastOfE, err := tc.DAGTopologyManager().IsAncestorOf(stagingArea, parent, e)
					if err != nil {
						t.Fatalf("Failed IsAncestorOf: %v", err)
					}
					if isInPastOfE {
						continue
					}
					// This is the bug - G has parent C, but C might not be in the result
					t.Errorf("MISSING PARENT BUG: Block %s needs parent %s but it's not in result and not in past of %s",
						blockHash, parent, e)
				}
			}
		}

		// Test brute version as well
		hashesBrute, _, err := tc.SyncManager().GetHashesBetween(stagingArea, e, h, math.MaxUint64, true)
		if err != nil {
			t.Fatalf("GetHashesBetween (brute) failed: %v", err)
		}

		t.Logf("Hashes between E and H (brute): %v", hashesBrute)

		blockSetBrute := make(map[string]bool)
		for _, hash := range hashesBrute {
			blockSetBrute[hash.String()] = true
		}

		for _, blockHash := range hashesBrute {
			header, err := tc.BlockHeaderStore().BlockHeader(tc.DatabaseContext(), stagingArea, blockHash)
			if err != nil {
				t.Fatalf("Failed to get header: %v", err)
			}

			for _, parentLevel := range header.Parents() {
				for _, parent := range parentLevel {
					if blockSetBrute[parent.String()] || parent.Equal(e) {
						continue
					}
					isInPastOfE, err := tc.DAGTopologyManager().IsAncestorOf(stagingArea, parent, e)
					if err != nil {
						t.Fatalf("Failed IsAncestorOf: %v", err)
					}
					if isInPastOfE {
						continue
					}
					t.Errorf("Brute version missing parent: Block %s needs parent %s", blockHash, parent)
				}
			}
		}
	})
}

// TestAntiPastHashesBetween_MultipleParallelChains tests with multiple parallel chains
func TestAntiPastHashesBetween_MultipleParallelChains(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		stagingArea := model.NewStagingArea()

		factory := consensus.NewFactory()
		tc, teardown, err := factory.NewTestConsensus(consensusConfig, "TestAntiPastHashesBetween_MultipleParallelChains")
		if err != nil {
			t.Fatalf("Error setting up consensus: %+v", err)
		}
		defer teardown(false)

		genesis := consensusConfig.GenesisHash

		a, _, err := tc.AddBlock([]*externalapi.DomainHash{genesis}, nil, nil)
		if err != nil {
			t.Fatalf("Failed adding block A: %v", err)
		}
		b, _, err := tc.AddBlock([]*externalapi.DomainHash{a}, nil, nil)
		if err != nil {
			t.Fatalf("Failed adding block B: %v", err)
		}

		c, _, err := tc.AddBlock([]*externalapi.DomainHash{genesis}, nil, nil)
		if err != nil {
			t.Fatalf("Failed adding block C: %v", err)
		}
		d, _, err := tc.AddBlock([]*externalapi.DomainHash{c}, nil, nil)
		if err != nil {
			t.Fatalf("Failed adding block D: %v", err)
		}

		e, _, err := tc.AddBlock([]*externalapi.DomainHash{b, d}, nil, nil)
		if err != nil {
			t.Fatalf("Failed adding block E: %v", err)
		}

		f, _, err := tc.AddBlock([]*externalapi.DomainHash{e}, nil, nil)
		if err != nil {
			t.Fatalf("Failed adding block F: %v", err)
		}

		hashes, actualHighHash, err := tc.SyncManager().GetHashesBetween(stagingArea, genesis, f, math.MaxUint64, false)
		if err != nil {
			t.Fatalf("GetHashesBetween failed: %v", err)
		}

		if !actualHighHash.Equal(f) {
			t.Fatalf("Expected actualHighHash: %s, got: %s", f, actualHighHash)
		}

		allBlocks := []*externalapi.DomainHash{a, b, c, d, e, f}
		blockSet := make(map[string]bool)
		for _, h := range hashes {
			blockSet[h.String()] = true
		}

		for _, expectedBlock := range allBlocks {
			if !blockSet[expectedBlock.String()] {
				t.Fatalf("Expected block %s to be in hashes", expectedBlock)
			}
		}
	})
}

// TestAntiPastHashesBetweenBrute_Basic tests the brute version with a simple chain
func TestAntiPastHashesBetweenBrute_Basic(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		stagingArea := model.NewStagingArea()

		factory := consensus.NewFactory()
		tc, teardown, err := factory.NewTestConsensus(consensusConfig, "TestAntiPastHashesBetweenBrute_Basic")
		if err != nil {
			t.Fatalf("Error setting up consensus: %+v", err)
		}
		defer teardown(false)

		chain := []*externalapi.DomainHash{consensusConfig.GenesisHash}
		tipHash := consensusConfig.GenesisHash
		for i := 0; i < 5; i++ {
			tipHash, _, err = tc.AddBlock([]*externalapi.DomainHash{tipHash}, nil, nil)
			if err != nil {
				t.Fatalf("Failed adding block: %v", err)
			}
			chain = append(chain, tipHash)
		}

		hashes, actualHighHash, err := tc.SyncManager().GetHashesBetween(stagingArea, chain[0], chain[5], math.MaxUint64, true)
		if err != nil {
			t.Fatalf("GetHashesBetween (brute) failed: %v", err)
		}

		// The brute version may return blocks in different order
		expected := chain[1:6]
		if len(hashes) != len(expected) {
			t.Fatalf("Expected %d hashes, got %d", len(expected), len(hashes))
		}

		// Convert to sets for comparison
		expectedSet := make(map[string]bool)
		for _, h := range expected {
			expectedSet[h.String()] = true
		}
		actualSet := make(map[string]bool)
		for _, h := range hashes {
			actualSet[h.String()] = true
		}
		for _, h := range expected {
			if !actualSet[h.String()] {
				t.Fatalf("Expected hash %s not found in result", h)
			}
		}

		if !actualHighHash.Equal(chain[5]) {
			t.Fatalf("Expected actualHighHash: %s\nActual: %s", chain[5], actualHighHash)
		}
	})
}

// TestAntiPastHashesBetweenBrute_ComplexDAG tests the brute version with a complex DAG
func TestAntiPastHashesBetweenBrute_ComplexDAG(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		stagingArea := model.NewStagingArea()

		factory := consensus.NewFactory()
		tc, teardown, err := factory.NewTestConsensus(consensusConfig, "TestAntiPastHashesBetweenBrute_ComplexDAG")
		if err != nil {
			t.Fatalf("Error setting up consensus: %+v", err)
		}
		defer teardown(false)

		genesis := consensusConfig.GenesisHash

		a, _, err := tc.AddBlock([]*externalapi.DomainHash{genesis}, nil, nil)
		if err != nil {
			t.Fatalf("Failed adding block A: %v", err)
		}

		b, _, err := tc.AddBlock([]*externalapi.DomainHash{a}, nil, nil)
		if err != nil {
			t.Fatalf("Failed adding block B: %v", err)
		}

		c, _, err := tc.AddBlock([]*externalapi.DomainHash{a}, nil, nil)
		if err != nil {
			t.Fatalf("Failed adding block C: %v", err)
		}

		d, _, err := tc.AddBlock([]*externalapi.DomainHash{b, c}, nil, nil)
		if err != nil {
			t.Fatalf("Failed adding block D: %v", err)
		}

		hashes, actualHighHash, err := tc.SyncManager().GetHashesBetween(stagingArea, genesis, d, math.MaxUint64, true)
		if err != nil {
			t.Fatalf("GetHashesBetween (brute) failed: %v", err)
		}

		if !actualHighHash.Equal(d) {
			t.Fatalf("Expected actualHighHash: %s\nActual: %s", d, actualHighHash)
		}

		allBlocks := []*externalapi.DomainHash{a, b, c, d}
		blockSet := make(map[string]bool)
		for _, h := range hashes {
			blockSet[h.String()] = true
		}

		for _, expectedBlock := range allBlocks {
			if !blockSet[expectedBlock.String()] {
				t.Fatalf("Expected block %s to be in hashes", expectedBlock)
			}
		}
	})
}

// TestAntiPastHashesBetween_LowHashNotInSelectedParentChain tests when lowHash needs adjustment
func TestAntiPastHashesBetween_LowHashNotInSelectedParentChain(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		stagingArea := model.NewStagingArea()

		factory := consensus.NewFactory()
		tc, teardown, err := factory.NewTestConsensus(consensusConfig, "TestAntiPastHashesBetween_LowHashNotInSelectedParentChain")
		if err != nil {
			t.Fatalf("Error setting up consensus: %+v", err)
		}
		defer teardown(false)

		genesis := consensusConfig.GenesisHash

		a, _, err := tc.AddBlock([]*externalapi.DomainHash{genesis}, nil, nil)
		if err != nil {
			t.Fatalf("Failed adding block A: %v", err)
		}

		b, _, err := tc.AddBlock([]*externalapi.DomainHash{a}, nil, nil)
		if err != nil {
			t.Fatalf("Failed adding block B: %v", err)
		}

		c, _, err := tc.AddBlock([]*externalapi.DomainHash{b}, nil, nil)
		if err != nil {
			t.Fatalf("Failed adding block C: %v", err)
		}

		side, _, err := tc.AddBlock([]*externalapi.DomainHash{genesis}, nil, nil)
		if err != nil {
			t.Fatalf("Failed adding side block: %v", err)
		}

		hashes, actualHighHash, err := tc.SyncManager().GetHashesBetween(stagingArea, side, c, math.MaxUint64, false)
		if err != nil {
			t.Fatalf("GetHashesBetween failed: %v", err)
		}

		if !actualHighHash.Equal(c) {
			t.Fatalf("Expected actualHighHash: %s\nActual: %s", c, actualHighHash)
		}

		blockSet := make(map[string]bool)
		for _, h := range hashes {
			blockSet[h.String()] = true
		}

		if !blockSet[a.String()] || !blockSet[b.String()] || !blockSet[c.String()] {
			t.Fatalf("Expected blocks A, B, C to be in hashes")
		}
	})
}

// TestAntiPastHashesBetween_InvalidMaxBlocks tests error with invalid maxBlocks
func TestAntiPastHashesBetween_InvalidMaxBlocks(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		stagingArea := model.NewStagingArea()

		factory := consensus.NewFactory()
		tc, teardown, err := factory.NewTestConsensus(consensusConfig, "TestAntiPastHashesBetween_InvalidMaxBlocks")
		if err != nil {
			t.Fatalf("Error setting up consensus: %+v", err)
		}
		defer teardown(false)

		tipHash, _, err := tc.AddBlock([]*externalapi.DomainHash{consensusConfig.GenesisHash}, nil, nil)
		if err != nil {
			t.Fatalf("Failed adding block: %v", err)
		}

		invalidMaxBlocks := tc.DAGParams().MergeSetSizeLimit
		_, _, err = tc.SyncManager().GetHashesBetween(stagingArea, consensusConfig.GenesisHash, tipHash, invalidMaxBlocks, false)
		if err == nil {
			t.Fatalf("Expected error for invalid maxBlocks, got nil")
		}
	})
}

// TestAntiPastHashesBetween_NonExistentBlock tests error with non-existent block
func TestAntiPastHashesBetween_NonExistentBlock(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		stagingArea := model.NewStagingArea()

		factory := consensus.NewFactory()
		tc, teardown, err := factory.NewTestConsensus(consensusConfig, "TestAntiPastHashesBetween_NonExistentBlock")
		if err != nil {
			t.Fatalf("Error setting up consensus: %+v", err)
		}
		defer teardown(false)

		nonExistentHash := &externalapi.DomainHash{}
		_, _, err = tc.SyncManager().GetHashesBetween(stagingArea, nonExistentHash, consensusConfig.GenesisHash, math.MaxUint64, false)
		if err == nil {
			t.Fatalf("Expected error for non-existent low hash, got nil")
		}
	})
}

// TestAntiPastHashesBetween_Consistency tests that both brute and regular versions return consistent results
func TestAntiPastHashesBetween_Consistency(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		stagingArea := model.NewStagingArea()

		factory := consensus.NewFactory()
		tc, teardown, err := factory.NewTestConsensus(consensusConfig, "TestAntiPastHashesBetween_Consistency")
		if err != nil {
			t.Fatalf("Error setting up consensus: %+v", err)
		}
		defer teardown(false)

		chain := []*externalapi.DomainHash{consensusConfig.GenesisHash}
		tipHash := consensusConfig.GenesisHash
		for i := 0; i < 5; i++ {
			tipHash, _, err = tc.AddBlock([]*externalapi.DomainHash{tipHash}, nil, nil)
			if err != nil {
				t.Fatalf("Failed adding block: %v", err)
			}
			chain = append(chain, tipHash)
		}

		hashesRegular, actualHighHashRegular, err := tc.SyncManager().GetHashesBetween(stagingArea, chain[0], chain[5], math.MaxUint64, false)
		if err != nil {
			t.Fatalf("GetHashesBetween (regular) failed: %v", err)
		}

		hashesBrute, actualHighHashBrute, err := tc.SyncManager().GetHashesBetween(stagingArea, chain[0], chain[5], math.MaxUint64, true)
		if err != nil {
			t.Fatalf("GetHashesBetween (brute) failed: %v", err)
		}

		if !actualHighHashRegular.Equal(actualHighHashBrute) {
			t.Fatalf("Expected same actualHighHash, got regular: %s, brute: %s", actualHighHashRegular, actualHighHashBrute)
		}

		if len(hashesRegular) != len(hashesBrute) {
			t.Fatalf("Expected same number of hashes, got regular: %d, brute: %d", len(hashesRegular), len(hashesBrute))
		}

		regularSet := make(map[string]bool)
		for _, h := range hashesRegular {
			regularSet[h.String()] = true
		}

		bruteSet := make(map[string]bool)
		for _, h := range hashesBrute {
			bruteSet[h.String()] = true
		}

		for h := range regularSet {
			if !bruteSet[h] {
				t.Fatalf("Hash %s found in regular but not in brute", h)
			}
		}

		for h := range bruteSet {
			if !regularSet[h] {
				t.Fatalf("Hash %s found in brute but not in regular", h)
			}
		}
	})
}

// TestAntiPastHashesBetween_WithMaxBlocks tests the maxBlocks limit
func TestAntiPastHashesBetween_WithMaxBlocks(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		stagingArea := model.NewStagingArea()

		factory := consensus.NewFactory()
		tc, teardown, err := factory.NewTestConsensus(consensusConfig, "TestAntiPastHashesBetween_WithMaxBlocks")
		if err != nil {
			t.Fatalf("Error setting up consensus: %+v", err)
		}
		defer teardown(false)

		chain := []*externalapi.DomainHash{consensusConfig.GenesisHash}
		tipHash := consensusConfig.GenesisHash
		for i := 0; i < 20; i++ {
			tipHash, _, err = tc.AddBlock([]*externalapi.DomainHash{tipHash}, nil, nil)
			if err != nil {
				t.Fatalf("Failed adding block: %v", err)
			}
			chain = append(chain, tipHash)
		}

		maxBlocks := tc.DAGParams().MergeSetSizeLimit + 10

		hashes, actualHighHash, err := tc.SyncManager().GetHashesBetween(stagingArea, chain[0], chain[20], maxBlocks, false)
		if err != nil {
			t.Fatalf("GetHashesBetween failed: %v", err)
		}

		if len(hashes) > int(maxBlocks) {
			t.Fatalf("Expected at most %d hashes, got %d", maxBlocks, len(hashes))
		}

		if !actualHighHash.Equal(chain[20]) {
			t.Fatalf("Expected actualHighHash: %s\nActual: %s", chain[20], actualHighHash)
		}
	})
}

// TestAntiPastHashesBetween_SelectedChildIterator tests the behavior with SelectedChildIterator
func TestAntiPastHashesBetween_SelectedChildIterator(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		stagingArea := model.NewStagingArea()

		factory := consensus.NewFactory()
		tc, teardown, err := factory.NewTestConsensus(consensusConfig, "TestAntiPastHashesBetween_SelectedChildIterator")
		if err != nil {
			t.Fatalf("Error setting up consensus: %+v", err)
		}
		defer teardown(false)

		chain := []*externalapi.DomainHash{consensusConfig.GenesisHash}
		tipHash := consensusConfig.GenesisHash
		for i := 0; i < 10; i++ {
			tipHash, _, err = tc.AddBlock([]*externalapi.DomainHash{tipHash}, nil, nil)
			if err != nil {
				t.Fatalf("Failed adding block: %v", err)
			}
			chain = append(chain, tipHash)
		}

		lowHash := chain[3]
		highHash := chain[7]

		hashes, actualHighHash, err := tc.SyncManager().GetHashesBetween(stagingArea, lowHash, highHash, math.MaxUint64, false)
		if err != nil {
			t.Fatalf("GetHashesBetween failed: %v", err)
		}

		// For a simple chain, the result should be the blocks between lowHash and highHash
		expected := chain[4:8]
		if len(hashes) != len(expected) {
			t.Fatalf("Expected %d hashes, got %d", len(expected), len(hashes))
		}

		// Verify all expected blocks are present
		expectedSet := make(map[string]bool)
		for _, h := range expected {
			expectedSet[h.String()] = true
		}
		for _, h := range hashes {
			if !expectedSet[h.String()] {
				t.Fatalf("Unexpected hash %s in result", h)
			}
		}

		if !actualHighHash.Equal(highHash) {
			t.Fatalf("Expected actualHighHash: %s\nActual: %s", highHash, actualHighHash)
		}
	})
}

// TestAntiPastHashesBetween_TopologicalOrder tests that the returned hashes are in topological order
func TestAntiPastHashesBetween_TopologicalOrder(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		stagingArea := model.NewStagingArea()

		factory := consensus.NewFactory()
		tc, teardown, err := factory.NewTestConsensus(consensusConfig, "TestAntiPastHashesBetween_TopologicalOrder")
		if err != nil {
			t.Fatalf("Error setting up consensus: %+v", err)
		}
		defer teardown(false)

		// Test with a linear chain to ensure basic topological ordering
		genesis := consensusConfig.GenesisHash
		chain := []*externalapi.DomainHash{genesis}
		tipHash := genesis
		for j := 0; j < 5; j++ {
			tipHash, _, err = tc.AddBlock([]*externalapi.DomainHash{tipHash}, nil, nil)
			if err != nil {
				t.Fatalf("Failed adding block: %v", err)
			}
			chain = append(chain, tipHash)
		}

		hashesChain, _, err := tc.SyncManager().GetHashesBetween(stagingArea, chain[0], chain[5], math.MaxUint64, false)
		if err != nil {
			t.Fatalf("GetHashesBetween (chain) failed: %v", err)
		}

		t.Logf("Hashes from linear chain: %v", hashesChain)

		// For a linear chain, the result should be in the same order as the chain (excluding lowHash, including highHash)
		// So we expect chain[1], chain[2], chain[3], chain[4], chain[5]
		expectedChain := chain[1:6]
		if len(hashesChain) != len(expectedChain) {
			t.Fatalf("Expected %d hashes for linear chain, got %d", len(expectedChain), len(hashesChain))
		}

		// Check that all expected blocks are present
		chainSet := make(map[string]bool)
		for _, h := range hashesChain {
			chainSet[h.String()] = true
		}
		for _, expected := range expectedChain {
			if !chainSet[expected.String()] {
				t.Fatalf("Expected hash %s not found in result. Result: %v", expected, hashesChain)
			}
		}

		// Check that no unexpected blocks are present
		if len(hashesChain) != len(expectedChain) {
			t.Fatalf("Expected %d hashes, got %d. Result: %v", len(expectedChain), len(hashesChain), hashesChain)
		}

		// Test 1: In a linear chain, verify topological order: for any i < j, hash[i] should be an ancestor of hash[j]
		for i, hashI := range hashesChain {
			for j, hashJ := range hashesChain[i+1:] {
				// hashI should be an ancestor of hashJ (since hashI comes before hashJ in the chain)
				isAncestor, err := tc.DAGTopologyManager().IsAncestorOf(stagingArea, hashI, hashJ)
				if err != nil {
					t.Fatalf("Failed IsAncestorOf check (chain): %v", err)
				}
				if !isAncestor {
					t.Errorf("TOPOLOGICAL ORDER VIOLATION (chain): Block at index %d (%s) is NOT an ancestor of block at index %d (%s). "+
						"Linear chain hashes are NOT in topological order!", i, hashI, i+1+j, hashJ)
				}
			}
		}

		// Test 2: Test with a complex DAG to verify topological order
		// Create DAG: genesis -> A -> B -> C
		//               \-> D -> E -> F
		//                         \-> G (parents: F, C)
		//                               \-> H

		a, _, err := tc.AddBlock([]*externalapi.DomainHash{genesis}, nil, nil)
		if err != nil {
			t.Fatalf("Failed adding block A: %v", err)
		}
		b, _, err := tc.AddBlock([]*externalapi.DomainHash{a}, nil, nil)
		if err != nil {
			t.Fatalf("Failed adding block B: %v", err)
		}
		c, _, err := tc.AddBlock([]*externalapi.DomainHash{b}, nil, nil)
		if err != nil {
			t.Fatalf("Failed adding block C: %v", err)
		}

		d, _, err := tc.AddBlock([]*externalapi.DomainHash{genesis}, nil, nil)
		if err != nil {
			t.Fatalf("Failed adding block D: %v", err)
		}
		e, _, err := tc.AddBlock([]*externalapi.DomainHash{d}, nil, nil)
		if err != nil {
			t.Fatalf("Failed adding block E: %v", err)
		}
		f, _, err := tc.AddBlock([]*externalapi.DomainHash{e}, nil, nil)
		if err != nil {
			t.Fatalf("Failed adding block F: %v", err)
		}

		// G has two parents: F and C (merge point)
		g, _, err := tc.AddBlock([]*externalapi.DomainHash{f, c}, nil, nil)
		if err != nil {
			t.Fatalf("Failed adding block G: %v", err)
		}

		h, _, err := tc.AddBlock([]*externalapi.DomainHash{g}, nil, nil)
		if err != nil {
			t.Fatalf("Failed adding block H: %v", err)
		}

		// Get hashes from genesis to H
		hashesDAG, _, err := tc.SyncManager().GetHashesBetween(stagingArea, genesis, h, math.MaxUint64, false)
		if err != nil {
			t.Fatalf("GetHashesBetween (DAG) failed: %v", err)
		}

		t.Logf("Hashes from DAG: %v", hashesDAG)

		// Test 3: Verify topological order for DAG
		// For any i < j, hashesDAG[i] should be an ancestor of hashesDAG[j] OR
		// they should be in concurrent branches (neither is ancestor of the other)
		for i, hashI := range hashesDAG {
			for j, hashJ := range hashesDAG[i+1:] {
				isAncestorIJ, err := tc.DAGTopologyManager().IsAncestorOf(stagingArea, hashI, hashJ)
				if err != nil {
					t.Fatalf("Failed IsAncestorOf check (DAG i->j): %v", err)
				}
				isAncestorJI, err := tc.DAGTopologyManager().IsAncestorOf(stagingArea, hashJ, hashI)
				if err != nil {
					t.Fatalf("Failed IsAncestorOf check (DAG j->i): %v", err)
				}

				// In topological order, we should never have j come before i if j is an ancestor of i
				// That would be a violation
				if isAncestorJI {
					t.Errorf("TOPOLOGICAL ORDER VIOLATION (DAG): Block at index %d (%s) has ancestor at index %d (%s) "+
						"coming AFTER it. This violates topological order!", i, hashI, i+1+j, hashJ)
				}

				// If neither is ancestor of the other, they are concurrent - this is fine
				// But we need to ensure the order is consistent with blue scores
				if !isAncestorIJ && !isAncestorJI {
					t.Logf("Blocks at indices %d (%s) and %d (%s) are concurrent (neither is ancestor of the other)",
						i, hashI, i+1+j, hashJ)
				}
			}
		}

		// Test 4: Test brute version as well
		hashesBrute, _, err := tc.SyncManager().GetHashesBetween(stagingArea, genesis, h, math.MaxUint64, true)
		if err != nil {
			t.Fatalf("GetHashesBetween (brute) failed: %v", err)
		}

		t.Logf("Hashes from DAG (brute): %v", hashesBrute)

		// Verify brute version is also topologically sorted
		for i, hashI := range hashesBrute {
			for j, hashJ := range hashesBrute[i+1:] {
				isAncestorJI, err := tc.DAGTopologyManager().IsAncestorOf(stagingArea, hashJ, hashI)
				if err != nil {
					t.Fatalf("Failed IsAncestorOf check (brute DAG): %v", err)
				}
				if isAncestorJI {
					t.Errorf("TOPOLOGICAL ORDER VIOLATION (brute): Block at index %d (%s) has ancestor at index %d (%s) "+
						"coming AFTER it!", i, hashI, i+1+j, hashJ)
				}
			}
		}
	})
}

// TestAntiPastHashesBetween_IBDScenario_MissingParent simulates the exact IBD scenario from the logs
// where block 3ba7ac9a1d0f8262ba05e3e5c00fab85ffc9ce9ed8ddfaec6709615af7a6c531
// has missing parents. This test creates a scenario where a block on a side chain
// has parents that are not in the selected parent chain of the lowHash.
func TestAntiPastHashesBetween_IBDScenario_MissingParent(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		stagingArea := model.NewStagingArea()

		factory := consensus.NewFactory()
		tc, teardown, err := factory.NewTestConsensus(consensusConfig, "TestAntiPastHashesBetween_IBDScenario_MissingParent")
		if err != nil {
			t.Fatalf("Error setting up consensus: %+v", err)
		}
		defer teardown(false)

		// Simulate the IBD scenario where a block has parents
		// that are in different branches.

		// Structure:
		// Genesis -> A -> B -> C -> D -> E -> F (main chain)
		//           \-> G -> H -> I -> J -> K
		//                   \-> L -> M (merge: L has parents H and C)
		//                         \-> N (parents: M and J)
		//                               \-> O

		genesis := consensusConfig.GenesisHash

		// Main chain
		a, _, err := tc.AddBlock([]*externalapi.DomainHash{genesis}, nil, nil)
		if err != nil {
			t.Fatalf("Failed adding block A: %v", err)
		}
		b, _, err := tc.AddBlock([]*externalapi.DomainHash{a}, nil, nil)
		if err != nil {
			t.Fatalf("Failed adding block B: %v", err)
		}
		c, _, err := tc.AddBlock([]*externalapi.DomainHash{b}, nil, nil)
		if err != nil {
			t.Fatalf("Failed adding block C: %v", err)
		}
		d, _, err := tc.AddBlock([]*externalapi.DomainHash{c}, nil, nil)
		if err != nil {
			t.Fatalf("Failed adding block D: %v", err)
		}
		e, _, err := tc.AddBlock([]*externalapi.DomainHash{d}, nil, nil)
		if err != nil {
			t.Fatalf("Failed adding block E: %v", err)
		}
		_, _, err = tc.AddBlock([]*externalapi.DomainHash{e}, nil, nil)
		if err != nil {
			t.Fatalf("Failed adding block F: %v", err)
		}

		// Side chain 1
		g, _, err := tc.AddBlock([]*externalapi.DomainHash{genesis}, nil, nil)
		if err != nil {
			t.Fatalf("Failed adding block G: %v", err)
		}
		h, _, err := tc.AddBlock([]*externalapi.DomainHash{g}, nil, nil)
		if err != nil {
			t.Fatalf("Failed adding block H: %v", err)
		}
		i, _, err := tc.AddBlock([]*externalapi.DomainHash{h}, nil, nil)
		if err != nil {
			t.Fatalf("Failed adding block I: %v", err)
		}
		j, _, err := tc.AddBlock([]*externalapi.DomainHash{i}, nil, nil)
		if err != nil {
			t.Fatalf("Failed adding block J: %v", err)
		}
		_, _, err = tc.AddBlock([]*externalapi.DomainHash{j}, nil, nil)
		if err != nil {
			t.Fatalf("Failed adding block K: %v", err)
		}

		// Merge point 1: L has parents H and C
		// C is on the main chain, H is on the side chain
		l, _, err := tc.AddBlock([]*externalapi.DomainHash{h, c}, nil, nil)
		if err != nil {
			t.Fatalf("Failed adding block L: %v", err)
		}

		// Side chain 2 from L
		m, _, err := tc.AddBlock([]*externalapi.DomainHash{l}, nil, nil)
		if err != nil {
			t.Fatalf("Failed adding block M: %v", err)
		}

		// Merge point 2: N has parents M and J
		n, _, err := tc.AddBlock([]*externalapi.DomainHash{m, j}, nil, nil)
		if err != nil {
			t.Fatalf("Failed adding block N: %v", err)
		}

		o, _, err := tc.AddBlock([]*externalapi.DomainHash{n}, nil, nil)
		if err != nil {
			t.Fatalf("Failed adding block O: %v", err)
		}

		// Test 1: Get hashes from G to O
		t.Logf("Testing IBD scenario: G to O")
		hashes, actualHighHash, err := tc.SyncManager().GetHashesBetween(stagingArea, g, o, math.MaxUint64, false)
		if err != nil {
			t.Fatalf("GetHashesBetween failed: %v", err)
		}
		if !actualHighHash.Equal(o) {
			t.Fatalf("Expected actualHighHash: %s, got: %s", o, actualHighHash)
		}

		t.Logf("Hashes between G and O: %v", hashes)

		// Build a set of returned hashes
		hashSet := make(map[string]bool)
		for _, h := range hashes {
			hashSet[h.String()] = true
		}

		// Check that for every block in the result, all its parents are either:
		// 1. In the result
		// 2. The lowHash (G)
		// 3. In the past of lowHash (G)
		for _, blockHash := range hashes {
			header, err := tc.BlockHeaderStore().BlockHeader(tc.DatabaseContext(), stagingArea, blockHash)
			if err != nil {
				t.Fatalf("Failed to get header for %s: %v", blockHash, err)
			}

			t.Logf("Checking block %s with parents %v", blockHash, header.Parents())

			for _, parentLevel := range header.Parents() {
				for _, parent := range parentLevel {
					if hashSet[parent.String()] || parent.Equal(g) {
						continue
					}
					isInPastOfG, err := tc.DAGTopologyManager().IsAncestorOf(stagingArea, parent, g)
					if err != nil {
						t.Fatalf("Failed IsAncestorOf check: %v", err)
					}
					if isInPastOfG {
						continue
					}
					// CRITICAL BUG: Parent is missing from result and not in G's past
					t.Errorf("IBD MISSING PARENT BUG: Block %s has parent %s that is NOT in result "+
						"and NOT in past of lowHash %s! This would cause ErrMissingParents during IBD!",
						blockHash, parent, g)
				}
			}
		}

		// Test 2: Get hashes from H to O (H is on the side chain)
		t.Logf("\nTesting with H as lowHash to O")
		hashes2, _, err := tc.SyncManager().GetHashesBetween(stagingArea, h, o, math.MaxUint64, false)
		if err != nil {
			t.Fatalf("GetHashesBetween (H to O) failed: %v", err)
		}

		t.Logf("Hashes between H and O: %v", hashes2)

		hashSet2 := make(map[string]bool)
		for _, h := range hashes2 {
			hashSet2[h.String()] = true
		}

		for _, blockHash := range hashes2 {
			header, err := tc.BlockHeaderStore().BlockHeader(tc.DatabaseContext(), stagingArea, blockHash)
			if err != nil {
				t.Fatalf("Failed to get header: %v", err)
			}

			for _, parentLevel := range header.Parents() {
				for _, parent := range parentLevel {
					if hashSet2[parent.String()] || parent.Equal(h) {
						continue
					}
					isInPastOfH, err := tc.DAGTopologyManager().IsAncestorOf(stagingArea, parent, h)
					if err != nil {
						t.Fatalf("Failed IsAncestorOf: %v", err)
					}
					if isInPastOfH {
						continue
					}
					t.Errorf("IBD BUG: Block %s has parent %s NOT in result and NOT in past of %s",
						blockHash, parent, h)
				}
			}
		}

		// Test 3: Test brute version
		t.Logf("\nTesting brute version from H to O")
		hashesBrute, _, err := tc.SyncManager().GetHashesBetween(stagingArea, h, o, math.MaxUint64, true)
		if err != nil {
			t.Fatalf("GetHashesBetween (brute) failed: %v", err)
		}

		t.Logf("Hashes between H and O (brute): %v", hashesBrute)

		hashSetBrute := make(map[string]bool)
		for _, h := range hashesBrute {
			hashSetBrute[h.String()] = true
		}

		for _, blockHash := range hashesBrute {
			header, err := tc.BlockHeaderStore().BlockHeader(tc.DatabaseContext(), stagingArea, blockHash)
			if err != nil {
				t.Fatalf("Failed to get header: %v", err)
			}

			for _, parentLevel := range header.Parents() {
				for _, parent := range parentLevel {
					if hashSetBrute[parent.String()] || parent.Equal(h) {
						continue
					}
					isInPastOfH, err := tc.DAGTopologyManager().IsAncestorOf(stagingArea, parent, h)
					if err != nil {
						t.Fatalf("Failed IsAncestorOf: %v", err)
					}
					if isInPastOfH {
						continue
					}
					t.Errorf("Brute version IBD BUG: Block %s has parent %s NOT in result and NOT in past of %s",
						blockHash, parent, h)
				}
			}
		}
	})
}
