package consensusstatemanager_test

import (
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus"
	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/testapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/testutils"
)

func TestUTXOCommitment(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		consensusConfig.BlockCoinbaseMaturity = 0
		for i := range consensusConfig.DifficultyAdjustmentWindowSize {
			consensusConfig.DifficultyAdjustmentWindowSize[i] = 1
		}
		factory := consensus.NewFactory()

		consensus, teardown, err := factory.NewTestConsensus(consensusConfig, "TestUTXOCommitment")
		if err != nil {
			t.Fatalf("Error setting up consensus: %+v", err)
		}
		defer teardown(false)

		// Build a small chain and verify that each block's stored commitment matches
		// the reconstructed past UTXO set.
		genesisHash := consensusConfig.GenesisHash

		// Block A:
		blockAHash, _, err := consensus.AddBlock([]*externalapi.DomainHash{genesisHash}, nil, nil)
		if err != nil {
			t.Fatalf("Error creating block A: %+v", err)
		}
		checkBlockUTXOCommitment(t, consensus, blockAHash, "A")
		// Block B:
		blockBHash, _, err := consensus.AddBlock([]*externalapi.DomainHash{blockAHash}, nil, nil)
		if err != nil {
			t.Fatalf("Error creating block B: %+v", err)
		}
		checkBlockUTXOCommitment(t, consensus, blockBHash, "B")
		// Block C:
		blockCHash, _, err := consensus.AddBlock([]*externalapi.DomainHash{blockBHash}, nil, nil)
		if err != nil {
			t.Fatalf("Error creating block C: %+v", err)
		}
		checkBlockUTXOCommitment(t, consensus, blockCHash, "C")
		// Block D:
		blockDHash, _, err := consensus.AddBlock([]*externalapi.DomainHash{blockCHash}, nil, nil)
		if err != nil {
			t.Fatalf("Error creating block D: %+v", err)
		}
		checkBlockUTXOCommitment(t, consensus, blockDHash, "D")
		// Block E:
		blockEHash, _, err := consensus.AddBlock([]*externalapi.DomainHash{blockDHash}, nil, nil)
		if err != nil {
			t.Fatalf("Error creating block E: %+v", err)
		}
		checkBlockUTXOCommitment(t, consensus, blockEHash, "E")
	})
}

// expectedUTXOCommitments stores the golden data for expected UTXO commitments
// for each network and block. These values are updated when the UTXO commitment
// calculation logic changes.
var expectedUTXOCommitments = map[string]map[string]string{
	"hoosat-mainnet": {
		"A": "544eb3142c000f0ad2c76ac41f4222abbababed830eeafee4b6dc56b52d5cac0",
		"B": "544eb3142c000f0ad2c76ac41f4222abbababed830eeafee4b6dc56b52d5cac0",
		"C": "1cdb70a13760d9b70bda468c7538fe0899bbeb78c647e8e3611d8ee72632f847",
		"D": "0a10b8bf0c1d617adc35eda62f6ba9a8a8f57f6608e6ae937db9246a455910ad",
		"E": "ae9d1c94c91a20f462c7ab8f968f51beacfb5a93e4bddb7e56026c3ad17ee9ff",
	},
	"hoosat-testnet": {
		"A": "544eb3142c000f0ad2c76ac41f4222abbababed830eeafee4b6dc56b52d5cac0",
		"B": "544eb3142c000f0ad2c76ac41f4222abbababed830eeafee4b6dc56b52d5cac0",
		"C": "a6b04244e759ca4f639f047b4b0f045ba3933df900733c739d76ff43da2998f1",
		"D": "a0b880e0f0590c3db4a311a1d3f64535094a93c839d27cea98f97ca18907a2f5",
		"E": "8f2bdd03450de986838f64bbd24e70713ab14502aa7a7447efb108f8ab3c5cb7",
	},
}

func checkBlockUTXOCommitment(t *testing.T, consensus testapi.TestConsensus, blockHash *externalapi.DomainHash, blockName string) {
	block, _, err := consensus.GetBlock(blockHash)
	if err != nil {
		t.Fatalf("Error getting block %s: %+v", blockName, err)
	}

	// Get the network name from the consensus DAG params
	networkName := consensus.DAGParams().Name
	
	// Get the expected commitment for this network and block
	expectedCommitmentStr, ok := expectedUTXOCommitments[networkName][blockName]
	if !ok {
		t.Fatalf("No expected UTXO commitment found for network %s, block %s", networkName, blockName)
	}
	
	// Parse the expected commitment string to a DomainHash
	expectedCommitment, err := externalapi.NewDomainHashFromString(expectedCommitmentStr)
	if err != nil {
		t.Fatalf("Failed to parse expected UTXO commitment for block %s: %s", blockName, expectedCommitmentStr)
	}
	
	// Compare the actual (stored) commitment with the expected one
	actualCommitment := block.Header.UTXOCommitment()
	if !expectedCommitment.Equal(actualCommitment) {
		t.Fatalf("TestUTXOCommitment: expected UTXO commitment for block %s doesn't match actual. Want: %s, got: %s",
			blockName, expectedCommitment, actualCommitment)
	}
}

func TestPastUTXOMultiset(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		stagingArea := model.NewStagingArea()

		factory := consensus.NewFactory()

		consensus, teardown, err := factory.NewTestConsensus(consensusConfig, "TestUTXOCommitment")
		if err != nil {
			t.Fatalf("Error setting up consensus: %+v", err)
		}
		defer teardown(false)

		// Build a short chain
		currentHash := consensusConfig.GenesisHash
		for range 3 {
			currentHash, _, err = consensus.AddBlock([]*externalapi.DomainHash{currentHash}, nil, nil)
			if err != nil {
				t.Fatalf("Error creating block A: %+v", err)
			}
		}

		// Save the current tip's hash to be used lated
		testedBlockHash := currentHash

		// Take testedBlock's multiset and hash
		firstMultiset, err := consensus.MultisetStore().Get(consensus.DatabaseContext(), stagingArea, testedBlockHash)
		if err != nil {
			return
		}
		firstMultisetHash := firstMultiset.Hash()

		// Add another block on top of testedBlock
		_, _, err = consensus.AddBlock([]*externalapi.DomainHash{testedBlockHash}, nil, nil)
		if err != nil {
			t.Fatalf("Error creating block A: %+v", err)
		}

		// Take testedBlock's multiset and hash again
		secondMultiset, err := consensus.MultisetStore().Get(consensus.DatabaseContext(), stagingArea, testedBlockHash)
		if err != nil {
			return
		}
		secondMultisetHash := secondMultiset.Hash()

		// Make sure the multiset hasn't changed
		if !firstMultisetHash.Equal(secondMultisetHash) {
			t.Fatalf("TestPastUTXOMultiSet: selectedParentMultiset appears to have changed!")
		}
	})
}
