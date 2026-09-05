package consensus_test

import (
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/multiset"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/testutils"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/utxo"
)

// TestIterateUTXOSetAtBlockFromAcceptanceData pins the property the exodus pruning point tooling
// depends on: the UTXO set rebuilt from a block's acceptance history hashes to that block's own
// header UTXO commitment.
//
// That is the only check that says a UTXO set is the one the chain committed to, as opposed to one
// that merely agrees with itself, and it is what makes this derivation - rather than virtual's
// materialised UTXO table, which is never recomputed once a UTXO diff has been applied to it - the
// right source for a snapshot that is going to be adopted as a trusted floor. On a healthy DAG the
// two derivations agree, which is also asserted here so that any future divergence between them
// shows up in tests rather than in a candidate bundle.
func TestIterateUTXOSetAtBlockFromAcceptanceData(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		consensusConfig.BlockCoinbaseMaturity = 0
		factory := consensus.NewFactory()
		tc, teardown, err := factory.NewTestConsensus(consensusConfig, "TestIterateUTXOSetAtBlockFromAcceptanceData")
		if err != nil {
			t.Fatalf("Error setting up consensus: %+v", err)
		}
		defer teardown(false)

		// A chain with a side block, so the blocks under test have merge sets holding more than
		// their selected parent and the replay has to stamp entries per merging block.
		blockA, _, err := tc.AddBlock([]*externalapi.DomainHash{consensusConfig.GenesisHash}, nil, nil)
		if err != nil {
			t.Fatalf("AddBlock A: %+v", err)
		}
		sideBlock, _, err := tc.AddBlock([]*externalapi.DomainHash{blockA}, nil, nil)
		if err != nil {
			t.Fatalf("AddBlock side: %+v", err)
		}
		blockB, _, err := tc.AddBlock([]*externalapi.DomainHash{blockA}, nil, nil)
		if err != nil {
			t.Fatalf("AddBlock B: %+v", err)
		}
		merger, _, err := tc.AddBlock([]*externalapi.DomainHash{blockB, sideBlock}, nil, nil)
		if err != nil {
			t.Fatalf("AddBlock merger: %+v", err)
		}
		tip, _, err := tc.AddBlock([]*externalapi.DomainHash{merger}, nil, nil)
		if err != nil {
			t.Fatalf("AddBlock tip: %+v", err)
		}

		for _, blockHash := range []*externalapi.DomainHash{merger, tip} {
			fromAcceptance := multiset.New()
			acceptanceCount := 0
			err := tc.IterateUTXOSetAtBlockFromAcceptanceData(blockHash,
				func(outpoint *externalapi.DomainOutpoint, entry externalapi.UTXOEntry) error {
					serialized, err := utxo.SerializeUTXO(entry, outpoint)
					if err != nil {
						return err
					}
					fromAcceptance.Add(serialized)
					acceptanceCount++
					return nil
				})
			if err != nil {
				t.Fatalf("IterateUTXOSetAtBlockFromAcceptanceData(%s): %+v", blockHash, err)
			}

			materialised := multiset.New()
			materialisedCount := 0
			err = tc.IterateUTXOSetAtBlock(blockHash,
				func(outpoint *externalapi.DomainOutpoint, entry externalapi.UTXOEntry) error {
					serialized, err := utxo.SerializeUTXO(entry, outpoint)
					if err != nil {
						return err
					}
					materialised.Add(serialized)
					materialisedCount++
					return nil
				})
			if err != nil {
				t.Fatalf("IterateUTXOSetAtBlock(%s): %+v", blockHash, err)
			}

			block, _, err := tc.GetBlock(blockHash)
			if err != nil {
				t.Fatalf("GetBlock(%s): %+v", blockHash, err)
			}
			expected := block.Header.UTXOCommitment()

			if !fromAcceptance.Hash().Equal(expected) {
				t.Fatalf("the UTXO set rebuilt from %s's acceptance data (%d entries) hashes to %s, but the "+
					"block's own header commits to %s - a bundle built from this derivation would not be the "+
					"set the chain committed to", blockHash, acceptanceCount, fromAcceptance.Hash(), expected)
			}
			if !materialised.Hash().Equal(fromAcceptance.Hash()) {
				t.Fatalf("the two derivations of %s's UTXO set disagree: acceptance data gives %s over %d "+
					"entries, the materialised table gives %s over %d entries",
					blockHash, fromAcceptance.Hash(), acceptanceCount, materialised.Hash(), materialisedCount)
			}
		}
	})
}
