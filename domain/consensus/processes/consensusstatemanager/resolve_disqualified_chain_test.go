package consensusstatemanager_test

import (
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/testapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/testutils"
	"github.com/HoosatNetwork/HTND/v2/util/staging"
)

// TestResolveVirtualInChunksOverDisqualifiedChain pins that resolving a disqualified chain in chunks leaves virtual's
// UTXO set where resolving it as a valid chain does. A disqualified block's past UTXO is calculated exactly like a valid
// one's - disqualification only means its header's commitment is not trusted - so the two sets must be identical.
//
// The chunk's resolve tip used to keep a diff relative to its selected parent, which updateSelectedTipUTXODiff then read
// as relative to virtual. Every chunk after the first started from a wrong past UTXO and committed a wrong virtual diff,
// and the error carried into every later chunk, which on mainnet showed up as IBD virtual resolution getting slower with
// every chunk.
func TestResolveVirtualInChunksOverDisqualifiedChain(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		factory := consensus.NewFactory()

		builder, teardownBuilder, err := factory.NewTestConsensus(consensusConfig,
			"TestResolveVirtualInChunksOverDisqualifiedChain_builder")
		if err != nil {
			t.Fatalf("Error setting up builder consensus: %+v", err)
		}
		defer teardownBuilder(false)

		const chainLength = 21
		chain := make([]*externalapi.DomainHash, 0, chainLength)
		tipHash := consensusConfig.GenesisHash
		for i := range chainLength {
			tipHash, _, err = builder.AddBlock([]*externalapi.DomainHash{tipHash}, nil, nil)
			if err != nil {
				t.Fatalf("Error adding block %d to the builder: %+v", i, err)
			}
			chain = append(chain, tipHash)
		}

		tc, teardown, err := factory.NewTestConsensus(consensusConfig, "TestResolveVirtualInChunksOverDisqualifiedChain")
		if err != nil {
			t.Fatalf("Error setting up consensus: %+v", err)
		}
		defer teardown(false)

		for i, blockHash := range chain {
			block, found, err := builder.GetBlock(blockHash)
			if err != nil || !found {
				t.Fatalf("Error getting block %d from the builder: found=%t err=%+v", i, found, err)
			}
			// The root is resolved on arrival and everything above it is left pending, as IBD leaves it.
			err = tc.ValidateAndInsertBlock(block, i == 0, true)
			if err != nil {
				t.Fatalf("Error inserting block %d: %+v", i, err)
			}
		}

		// Disqualify the root, so that every chain block above it resolves through ResolveBlockStatus's cascade branch.
		stagingArea := model.NewStagingArea()
		tc.BlockStatusStore().Stage(stagingArea, chain[0], externalapi.StatusDisqualifiedFromChain)
		err = staging.CommitAllChanges(tc.DatabaseContext(), stagingArea)
		if err != nil {
			t.Fatalf("Error disqualifying the root: %+v", err)
		}

		for chunk := 0; !virtualSelectedParent(t, tc).Equal(tipHash); chunk++ {
			if chunk > chainLength {
				t.Fatalf("virtual did not reach the chain tip after %d chunks", chunk)
			}
			_, _, err = tc.ResolveVirtualWithMaxParam(4)
			if err != nil {
				t.Fatalf("Error resolving chunk %d: %+v", chunk, err)
			}
		}

		status, err := tc.BlockStatusStore().Get(tc.DatabaseContext(), model.NewStagingArea(), tipHash)
		if err != nil {
			t.Fatalf("Error getting the tip's status: %+v", err)
		}
		if status != externalapi.StatusDisqualifiedFromChain {
			t.Fatalf("expected the tip to be disqualified by inheritance, got %s", status)
		}

		expected := virtualUTXOSet(t, builder)
		actual := virtualUTXOSet(t, tc)
		for outpoint, expectedEntry := range expected {
			actualEntry, ok := actual[outpoint]
			if !ok {
				t.Errorf("virtual UTXO set is missing %s:%d", outpoint.TransactionID, outpoint.Index)
				continue
			}
			if !actualEntry.Equal(expectedEntry) {
				t.Errorf("virtual UTXO set holds %s:%d as %+v, expected %+v",
					outpoint.TransactionID, outpoint.Index, actualEntry, expectedEntry)
			}
		}
		for outpoint := range actual {
			if _, ok := expected[outpoint]; !ok {
				t.Errorf("virtual UTXO set holds %s:%d, which the chain's past does not",
					outpoint.TransactionID, outpoint.Index)
			}
		}
	})
}

func virtualSelectedParent(t *testing.T, tc testapi.TestConsensus) *externalapi.DomainHash {
	t.Helper()
	virtualGHOSTDAGData, err := tc.GHOSTDAGDataStore().Get(tc.DatabaseContext(), model.NewStagingArea(),
		model.VirtualBlockHash, false)
	if err != nil {
		t.Fatalf("Error getting virtual's GHOSTDAG data: %+v", err)
	}
	return virtualGHOSTDAGData.SelectedParent()
}

func virtualUTXOSet(t *testing.T, tc testapi.TestConsensus) map[externalapi.DomainOutpoint]externalapi.UTXOEntry {
	t.Helper()
	pairs, err := tc.ConsensusStateStore().VirtualUTXOs(tc.DatabaseContext(), nil, 100_000)
	if err != nil {
		t.Fatalf("Error reading the virtual UTXO set: %+v", err)
	}
	set := make(map[externalapi.DomainOutpoint]externalapi.UTXOEntry, len(pairs))
	for _, pair := range pairs {
		set[*pair.Outpoint] = pair.UTXOEntry
	}
	return set
}
