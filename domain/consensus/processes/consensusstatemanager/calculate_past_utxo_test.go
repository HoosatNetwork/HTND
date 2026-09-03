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
		"C": "df82b779639675289429fcbd7dcaa907b5d32a1c88d4be1b826c71313c011bfe",
		"D": "c9574ea9e190f2e7803498d84a9890de33776b9918f709ebae632f42865db92f",
		"E": "44e4a7f8e152bcef1e345bcea16658b544521f2895e93c445ad300a9a5add7a7",
	},
	"hoosat-testnet": {
		"A": "544eb3142c000f0ad2c76ac41f4222abbababed830eeafee4b6dc56b52d5cac0",
		"B": "544eb3142c000f0ad2c76ac41f4222abbababed830eeafee4b6dc56b52d5cac0",
		"C": "e167c35a1a601d72b3e0f0681a3c0fe26c49f45c1905b5ca5a54b843d8cc566a",
		"D": "faff3a4dffea1fe5f719dacd2c2c959effad97a3d289c1634fb8a3c46f3dd1f6",
		"E": "96f667be6a98fa90c7a88dc7af3e5303fc8f7bfa2960e514bef87b88deb65588",
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
