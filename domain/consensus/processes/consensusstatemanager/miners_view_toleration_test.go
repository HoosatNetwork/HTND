package consensusstatemanager_test

import (
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/testapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/blockheader"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/merkle"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/testutils"
)

// TestMinersViewFieldsToleratedOnCleanBaseline pins that a node whose baseline is NOT offset still
// tolerates a block whose only fault is its UTXO commitment or its accepted-ID merkle root.
//
// Mainnet mining nodes commit different UTXO histories. Enforcing these two fields only when the
// pruning point's own multiset happened to disagree with its header made enforcement depend on which
// node mined the pruning point: a node whose pruning point came from its own history disqualified
// every block templated on another, and split itself off the network.
//
// A fresh test consensus is on genesis, so its baseline check reports "not offset" - the situation
// that node was in. A block with a wrong coinbase must still be disqualified: that field moves value.
func TestMinersViewFieldsToleratedOnCleanBaseline(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		tc, teardown, err := consensus.NewFactory().NewTestConsensus(consensusConfig,
			"TestMinersViewFieldsToleratedOnCleanBaseline")
		if err != nil {
			t.Fatalf("Error setting up consensus: %+v", err)
		}
		defer teardown(false)

		health, err := tc.UTXOSetHealth()
		if err != nil {
			t.Fatalf("UTXOSetHealth: %+v", err)
		}
		if health.Checked && !health.BaselineVerified {
			t.Fatalf("expected a fresh consensus not to be on an offset baseline")
		}

		parent := consensusConfig.GenesisHash
		bogus := externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{0xba, 0xd})

		for _, field := range []string{"utxo-commitment", "accepted-id-merkle-root"} {
			block, _, err := tc.BuildBlockWithParents([]*externalapi.DomainHash{parent}, nil, nil)
			if err != nil {
				t.Fatalf("%s: BuildBlockWithParents: %+v", field, err)
			}
			h := block.Header
			acceptedIDMerkleRoot, utxoCommitment := h.AcceptedIDMerkleRoot(), h.UTXOCommitment()
			if field == "utxo-commitment" {
				utxoCommitment = bogus
			} else {
				acceptedIDMerkleRoot = bogus
			}
			block.Header = blockheader.NewImmutableBlockHeader(h.Version(), h.Parents(), h.HashMerkleRoot(),
				acceptedIDMerkleRoot, utxoCommitment, h.TimeInMilliseconds(), h.Bits(), h.Nonce(), h.DAAScore(),
				h.BlueScore(), h.BlueWork(), h.PruningPoint())

			err = tc.ValidateAndInsertBlock(block, true, true)
			if err != nil {
				t.Fatalf("%s: ValidateAndInsertBlock: %+v", field, err)
			}
			blockHash := consensushashing.BlockHash(block)
			if status := blockStatus(t, tc, blockHash); status != externalapi.StatusUTXOValid {
				t.Fatalf("%s: expected a block whose only fault is its %s to be tolerated, got status %s",
					field, field, status)
			}
			parent = blockHash
		}

		block, _, err := tc.BuildBlockWithParents([]*externalapi.DomainHash{parent}, nil, nil)
		if err != nil {
			t.Fatalf("BuildBlockWithParents for strict gate: %+v", err)
		}
		h := block.Header
		block.Header = blockheader.NewImmutableBlockHeader(h.Version(), h.Parents(), h.HashMerkleRoot(),
			h.AcceptedIDMerkleRoot(), bogus, h.TimeInMilliseconds(), h.Bits(), h.Nonce(), h.DAAScore(),
			h.BlueScore(), h.BlueWork(), h.PruningPoint())
		previousGate := tc.HardForkGates().StrictMinersViewFieldsVersion
		tc.HardForkGates().StrictMinersViewFieldsVersion = 1
		err = tc.ValidateAndInsertBlock(block, true, true)
		tc.HardForkGates().StrictMinersViewFieldsVersion = previousGate
		if err != nil {
			t.Fatalf("strict gate ValidateAndInsertBlock: %+v", err)
		}
		strictHash := consensushashing.BlockHash(block)
		if status := blockStatus(t, tc, strictHash); status != externalapi.StatusDisqualifiedFromChain {
			t.Fatalf("strict gate accepted a block with a wrong UTXO commitment: %s", status)
		}

		// A coinbase that pays one sompi more than its merge set earns. Everything else about the block,
		// both commitments included, is what the block builder produced.
		block, _, err = tc.BuildBlockWithParents([]*externalapi.DomainHash{parent}, nil, nil)
		if err != nil {
			t.Fatalf("BuildBlockWithParents: %+v", err)
		}
		block.Transactions[0].Outputs[0].Value++
		mutableHeader := block.Header.ToMutable()
		mutableHeader.SetHashMerkleRoot(merkle.CalculateHashMerkleRoot(block.Transactions))
		block.Header = mutableHeader.ToImmutable()
		err = tc.ValidateAndInsertBlock(block, true, true)
		if err != nil {
			t.Fatalf("ValidateAndInsertBlock of the overpaying block: %+v", err)
		}
		overpayingHash := consensushashing.BlockHash(block)
		if status := blockStatus(t, tc, overpayingHash); status != externalapi.StatusDisqualifiedFromChain {
			t.Fatalf("expected a block with a wrong coinbase to still be disqualified, got status %s", status)
		}
	})
}

func blockStatus(t *testing.T, tc testapi.TestConsensus, blockHash *externalapi.DomainHash) externalapi.BlockStatus {
	t.Helper()
	status, err := tc.BlockStatusStore().Get(tc.DatabaseContext(), model.NewStagingArea(), blockHash)
	if err != nil {
		t.Fatalf("Error getting the status of %s: %+v", blockHash, err)
	}
	return status
}
