package consensus_test

import (
	"math"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/testutils"
)

// TestDeletionDepthKeepsTheBlocksOfRecentPruningPoints pins that --deletion-depth changes which blocks pruning
// deletes. The value was plumbed into the pruning manager, but nothing called the function that applies it, so a node
// configured to keep the blocks of its last pruning points deleted exactly what a default node deletes.
func TestDeletionDepthKeepsTheBlocksOfRecentPruningPoints(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		defer constants.ForceSetBlockVersion(1)

		// Every block is version 1, with a finality depth of 5 blocks and 10-block DAA windows, so on a chain the
		// pruning point advances every 5 blocks and keeps only a short window below it.
		consensusConfig.POWScores = []uint64{math.MaxUint64}
		consensusConfig.K = append([]externalapi.KType(nil), consensusConfig.K...)
		consensusConfig.K[0] = 0
		consensusConfig.FinalityDuration = []time.Duration{5 * consensusConfig.TargetTimePerBlock[0]}
		consensusConfig.PruningProofM = 1
		// As TestPruning does: short DAA windows would otherwise raise the difficulty of these fast test blocks until
		// the target underflows.
		consensusConfig.DisableDifficultyAdjustment = true
		consensusConfig.DifficultyAdjustmentWindowSize = append([]int(nil), consensusConfig.DifficultyAdjustmentWindowSize...)
		for i := range consensusConfig.DifficultyAdjustmentWindowSize {
			consensusConfig.DifficultyAdjustmentWindowSize[i] = 10
		}

		factory := consensus.NewFactory()
		tcDefault, teardownDefault, err := factory.NewTestConsensus(consensusConfig, "TestDeletionDepthDefault")
		if err != nil {
			t.Fatalf("NewTestConsensus: %+v", err)
		}
		defer teardownDefault(false)

		deepConfig := *consensusConfig
		deepConfig.DeletionDepth = 3
		tcDeep, teardownDeep, err := factory.NewTestConsensus(&deepConfig, "TestDeletionDepthDeep")
		if err != nil {
			t.Fatalf("NewTestConsensus: %+v", err)
		}
		defer teardownDeep(false)

		chain := []*externalapi.DomainHash{consensusConfig.GenesisHash}
		for i := range 200 {
			block, _, err := tcDefault.BuildBlockWithParents([]*externalapi.DomainHash{chain[len(chain)-1]}, nil, nil)
			if err != nil {
				t.Fatalf("BuildBlockWithParents #%d: %+v", i, err)
			}
			for _, tc := range []interface {
				ValidateAndInsertBlock(*externalapi.DomainBlock, bool, bool) error
			}{tcDefault, tcDeep} {
				if err := tc.ValidateAndInsertBlock(block, true, true); err != nil {
					t.Fatalf("ValidateAndInsertBlock #%d: %+v", i, err)
				}
			}
			hash, err := tcDefault.GetVirtualSelectedParent()
			if err != nil {
				t.Fatalf("GetVirtualSelectedParent: %+v", err)
			}
			chain = append(chain, hash)
		}

		hasBlock := func(tc interface {
			BlockStore() model.BlockStore
			DatabaseContext() model.DBManager
		}, hash *externalapi.DomainHash) bool {
			has, err := tc.BlockStore().HasBlock(tc.DatabaseContext(), model.NewStagingArea(), hash)
			if err != nil {
				t.Fatalf("HasBlock: %+v", err)
			}
			return has
		}

		highestDeleted := -1
		for height := len(chain) - 1; height > 0; height-- {
			if !hasBlock(tcDefault, chain[height]) {
				highestDeleted = height
				break
			}
		}
		if highestDeleted < 0 {
			t.Fatalf("setup: the default node deleted no blocks, so there is nothing to compare")
		}
		if !hasBlock(tcDeep, chain[highestDeleted]) {
			t.Fatalf("a node with --deletion-depth=3 deleted chain block %d, which the default node's pruning point "+
				"deleted too: the setting had no effect", highestDeleted)
		}
		if hasBlock(tcDeep, chain[1]) {
			t.Fatalf("a node with --deletion-depth=3 never deleted anything: chain block 1 is still stored")
		}
	})
}
