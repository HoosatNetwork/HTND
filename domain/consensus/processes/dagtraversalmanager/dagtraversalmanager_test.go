package dagtraversalmanager_test

import (
	"testing"
	"time"

	"github.com/Hoosat-Oy/HTND/domain/consensus/model"

	"github.com/Hoosat-Oy/HTND/domain/consensus"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/constants"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/testutils"
)

func TestLowestChainBlockAboveOrEqualToBlueScore(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		consensusConfig.FinalityDuration = []time.Duration{10 * consensusConfig.TargetTimePerBlock[constants.GetBlockVersion()-1]}
		factory := consensus.NewFactory()
		tc, tearDown, err := factory.NewTestConsensus(consensusConfig,
			"TestLowestChainBlockAboveOrEqualToBlueScore")
		if err != nil {
			t.Fatalf("NewTestConsensus: %s", err)
		}
		defer tearDown(false)

		stagingArea := model.NewStagingArea()

		getBlueScore := func(blockHash *externalapi.DomainHash) uint64 {
			ghostdagData, err := tc.GHOSTDAGDataStore().Get(tc.DatabaseContext(), stagingArea, blockHash, false)
			if err != nil {
				t.Fatalf("GHOSTDAGDataStore().Get: %+v", err)
			}
			return ghostdagData.BlueScore()
		}

		// Build a simple chain
		tipHash := consensusConfig.GenesisHash
		for i := 0; i < 10; i++ {
			var err error
			tipHash, _, err = tc.AddBlock([]*externalapi.DomainHash{tipHash}, nil, nil)
			if err != nil {
				t.Fatalf("AddBlock: %+v", err)
			}
		}

		// Collect the selected parent chain from tip to genesis
		var selectedChain []*externalapi.DomainHash
		current := tipHash
		for !current.Equal(consensusConfig.GenesisHash) {
			selectedChain = append(selectedChain, current)
			ghostdagData, err := tc.GHOSTDAGDataStore().Get(tc.DatabaseContext(), stagingArea, current, false)
			if err != nil {
				t.Fatalf("GHOSTDAGDataStore().Get: %+v", err)
			}
			current = ghostdagData.SelectedParent()
		}
		selectedChain = append(selectedChain, consensusConfig.GenesisHash)

		// Reverse the chain so it goes from genesis to tip
		for i, j := 0, len(selectedChain)-1; i < j; i, j = i+1, j-1 {
			selectedChain[i], selectedChain[j] = selectedChain[j], selectedChain[i]
		}

		// Test LowestChainBlockAboveOrEqualToBlueScore
		// For blue score 0, it should return genesis
		result, err := tc.DAGTraversalManager().LowestChainBlockAboveOrEqualToBlueScore(stagingArea, tipHash, 0)
		if err != nil {
			t.Fatalf("LowestChainBlockAboveOrEqualToBlueScore: %+v", err)
		}
		if !result.Equal(consensusConfig.GenesisHash) {
			t.Fatalf("Expected genesis for blue score 0, got %s", result)
		}

		// For each block in the selected chain, verify the function returns the correct block
		for _, blockHash := range selectedChain {
			blueScore := getBlueScore(blockHash)
			result, err := tc.DAGTraversalManager().LowestChainBlockAboveOrEqualToBlueScore(stagingArea, tipHash, blueScore)
			if err != nil {
				t.Fatalf("LowestChainBlockAboveOrEqualToBlueScore: %+v", err)
			}
			resultBlueScore := getBlueScore(result)
			if resultBlueScore < blueScore {
				t.Fatalf("Expected block with blue score >= %d, got block with blue score %d", blueScore, resultBlueScore)
			}
		}

		// Test with blue score slightly below some blocks
		tipBlueScore := getBlueScore(tipHash)
		if tipBlueScore > 1 {
			result, err = tc.DAGTraversalManager().LowestChainBlockAboveOrEqualToBlueScore(stagingArea, tipHash, tipBlueScore-1)
			if err != nil {
				t.Fatalf("LowestChainBlockAboveOrEqualToBlueScore: %+v", err)
			}
			resultBlueScore := getBlueScore(result)
			if resultBlueScore < tipBlueScore-1 {
				t.Fatalf("Expected block with blue score >= %d, got block with blue score %d", tipBlueScore-1, resultBlueScore)
			}
		}
	})
}
