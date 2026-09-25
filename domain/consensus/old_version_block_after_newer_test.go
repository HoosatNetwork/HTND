package consensus_test

import (
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/testutils"
)

// TestOldVersionBlockAfterNewerVersionWasSeen pins that a block is validated against the version its
// own DAA score calls for, however far the process-global version has been ratcheted by blocks seen
// before it.
//
// A legitimately old-version block - a side-chain block below a version boundary, relayed or merged
// after the node has already seen blocks past it - must be accepted and judged as that version. The
// global only ever increases and depends on uptime and on what this node happened to see first, so
// any rule reading it for a block would make nodes disagree about the same block.
func TestOldVersionBlockAfterNewerVersionWasSeen(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		defer constants.ForceSetBlockVersion(1)

		// Version 1 below genesis DAA score + 4, version 2 from there.
		boundary := consensusConfig.GenesisBlock.Header.DAAScore() + 4
		consensusConfig.POWScores = []uint64{boundary}

		factory := consensus.NewFactory()
		builder, teardownBuilder, err := factory.NewTestConsensus(consensusConfig,
			"TestOldVersionBlockAfterNewerVersionWasSeen_builder")
		if err != nil {
			t.Fatalf("NewTestConsensus: %+v", err)
		}
		defer teardownBuilder(false)

		chain := []*externalapi.DomainHash{consensusConfig.GenesisHash}
		for i := 0; i < 8; i++ {
			tip, _, err := builder.AddBlock([]*externalapi.DomainHash{chain[len(chain)-1]}, nil, nil)
			if err != nil {
				t.Fatalf("AddBlock %d: %+v", i, err)
			}
			chain = append(chain, tip)
		}
		// The side block sits on chain[1], so its own DAA score is below the boundary.
		sideHash, _, err := builder.AddBlock([]*externalapi.DomainHash{chain[1]}, nil, nil)
		if err != nil {
			t.Fatalf("AddBlock side: %+v", err)
		}

		tc, teardown, err := factory.NewTestConsensus(consensusConfig, "TestOldVersionBlockAfterNewerVersionWasSeen")
		if err != nil {
			t.Fatalf("NewTestConsensus: %+v", err)
		}
		defer teardown(false)

		for i, hash := range chain[1:] {
			block, found, err := builder.GetBlock(hash)
			if err != nil || !found {
				t.Fatalf("GetBlock %d: found=%t err=%+v", i, found, err)
			}
			if err := tc.ValidateAndInsertBlock(block, true, true); err != nil {
				t.Fatalf("ValidateAndInsertBlock %d: %+v", i, err)
			}
		}
		tipBlock, _, err := builder.GetBlock(chain[len(chain)-1])
		if err != nil {
			t.Fatalf("GetBlock tip: %+v", err)
		}
		if tipBlock.Header.Version() != 2 || tipBlock.Header.DAAScore() < boundary {
			t.Fatalf("test setup: expected the main chain to cross the boundary, tip is version %d at DAA %d",
				tipBlock.Header.Version(), tipBlock.Header.DAAScore())
		}

		// This node has since seen (or built) something far newer.
		constants.ForceSetBlockVersion(10)

		side, found, err := builder.GetBlock(sideHash)
		if err != nil || !found {
			t.Fatalf("GetBlock side: found=%t err=%+v", found, err)
		}
		if side.Header.Version() != 1 || side.Header.DAAScore() >= boundary {
			t.Fatalf("test setup: expected a version-1 side block below the boundary, got version %d at DAA %d",
				side.Header.Version(), side.Header.DAAScore())
		}
		if err := tc.ValidateAndInsertBlock(side, true, true); err != nil {
			t.Fatalf("an old-version block was judged by the process-global version: %+v", err)
		}
		status, err := tc.GetBlockInfo(sideHash)
		if err != nil {
			t.Fatalf("GetBlockInfo: %+v", err)
		}
		if status.BlockStatus == externalapi.StatusInvalid || status.BlockStatus == externalapi.StatusDisqualifiedFromChain {
			t.Fatalf("the old-version block resolved to %s", status.BlockStatus)
		}

		// And a block merging it, built after the ratchet, is accepted as well.
		if _, _, err := tc.AddBlock([]*externalapi.DomainHash{chain[len(chain)-1], sideHash}, nil, nil); err != nil {
			t.Fatalf("a block merging the old-version block was rejected: %+v", err)
		}
	})
}
