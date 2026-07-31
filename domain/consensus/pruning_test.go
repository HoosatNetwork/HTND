package consensus_test

import (
	"strconv"
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/testutils"
)

func TestPruningDepth(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		// ForAllNets resets block version to 1 for each subtest.
		// For versions < 5, pruning depth is computed from finality depth, K and merge-set-size.
		finalityDepth, err := strconv.ParseUint(strconv.FormatInt(int64(consensusConfig.FinalityDuration[0]/consensusConfig.TargetTimePerBlock[0]), 10), 10, 64)
		if err != nil {
			t.Fatalf("failed converting finality depth: %v", err)
		}
		expected := 2*finalityDepth + 4*consensusConfig.MergeSetSizeLimit*uint64(consensusConfig.K[0]) + 2*uint64(consensusConfig.K[0]) + 2
		if consensusConfig.PruningDepth() != expected {
			t.Errorf("pruningDepth in %s is expected to be %d but got %d", consensusConfig.Name, expected, consensusConfig.PruningDepth())
		}
	})
}
