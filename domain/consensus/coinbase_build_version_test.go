package consensus_test

import (
	"math"
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/testutils"
)

// TestBuiltCoinbaseFollowsTheBuiltBlocksVersion pins that the coinbase of a block under construction is built for
// that block's own version. The coinbase manager fell back to the process-global block version for a block with no
// stored header yet - exactly the block being built - while validation uses the header version, so a node whose
// global was ahead of the chain built coinbases it rejected itself. The global can be raised by an unvalidated
// relayed header, so a peer could make a node build invalid block templates.
func TestBuiltCoinbaseFollowsTheBuiltBlocksVersion(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		defer constants.ForceSetBlockVersion(1)

		// Every block is version 1.
		consensusConfig.POWScores = []uint64{math.MaxUint64}

		tc, teardown, err := consensus.NewFactory().NewTestConsensus(consensusConfig, "TestBuiltCoinbaseOwnVersion")
		if err != nil {
			t.Fatalf("NewTestConsensus: %+v", err)
		}
		defer teardown(false)

		tip, _, err := tc.AddBlock([]*externalapi.DomainHash{consensusConfig.GenesisHash}, nil, nil)
		if err != nil {
			t.Fatalf("AddBlock: %+v", err)
		}

		constants.ForceSetBlockVersion(9)
		if _, _, err := tc.AddBlock([]*externalapi.DomainHash{tip}, nil, nil); err != nil {
			t.Fatalf("a version-1 block built with the global at 9 was rejected: %+v", err)
		}
		template, err := tc.BuildBlock(&externalapi.DomainCoinbaseData{ScriptPublicKey: &externalapi.ScriptPublicKey{}}, nil)
		if err != nil {
			t.Fatalf("BuildBlock: %+v", err)
		}
		if err := tc.ValidateAndInsertBlock(template, true, true); err != nil {
			t.Fatalf("the node rejected its own version-%d block template built with the global at 9: %+v",
				template.Header.Version(), err)
		}
	})
}
