package consensus_test

import (
	"errors"
	"math"
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/ruleerrors"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/testutils"
)

// TestVirtualParentsFollowTheNextBlocksVersion pins that virtual's parents - which become a mined block's parents -
// respect the parents limit of the version the next block will have. The limit was indexed by the process-global
// block version, so a node whose global had advanced past a version-1 chain picked up to 12 parents where header
// validation, following the block's own version, allows 10: it built templates every node rejects.
func TestVirtualParentsFollowTheNextBlocksVersion(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		defer constants.ForceSetBlockVersion(1)

		// Every block is version 1, whose limit is MaxBlockParents[0]; later versions allow more.
		consensusConfig.POWScores = []uint64{math.MaxUint64}
		versionOneLimit := int(consensusConfig.MaxBlockParents[0])
		if int(consensusConfig.MaxBlockParents[len(consensusConfig.MaxBlockParents)-1]) <= versionOneLimit {
			t.Skipf("%s does not raise the parents limit in later versions", consensusConfig.Name)
		}

		tc, teardown, err := consensus.NewFactory().NewTestConsensus(consensusConfig, "TestVirtualParentsNextBlockVersion")
		if err != nil {
			t.Fatalf("NewTestConsensus: %+v", err)
		}
		defer teardown(false)

		base, _, err := tc.AddBlock([]*externalapi.DomainHash{consensusConfig.GenesisHash}, nil, nil)
		if err != nil {
			t.Fatalf("AddBlock: %+v", err)
		}
		for i := range versionOneLimit + 2 {
			if _, _, err := tc.AddBlock([]*externalapi.DomainHash{base}, nil, nil); err != nil {
				t.Fatalf("AddBlock tip #%d: %+v", i, err)
			}
		}

		// The last tip is built as usual but arrives on a node whose global version has advanced, so virtual re-picks
		// its parents then. (Building with the global ahead of the chain is a separate issue: see HTN-003 coinbase.)
		lastTip, _, err := tc.BuildBlockWithParents([]*externalapi.DomainHash{base}, nil, nil)
		if err != nil {
			t.Fatalf("BuildBlockWithParents: %+v", err)
		}
		constants.ForceSetBlockVersion(9)
		err = tc.ValidateAndInsertBlock(lastTip, true, true)
		constants.ForceSetBlockVersion(1)
		if err != nil {
			t.Fatalf("ValidateAndInsertBlock last tip: %+v", err)
		}

		block, err := tc.BuildBlock(&externalapi.DomainCoinbaseData{ScriptPublicKey: &externalapi.ScriptPublicKey{}}, nil)
		if err != nil {
			t.Fatalf("BuildBlock: %+v", err)
		}
		if parents := len(block.Header.DirectParents()); parents > versionOneLimit {
			t.Errorf("a version-%d block template has %d parents, above that version's limit of %d",
				block.Header.Version(), parents, versionOneLimit)
		}
		if err := tc.ValidateAndInsertBlock(block, true, true); errors.Is(err, ruleerrors.ErrTooManyParents) {
			t.Errorf("the node rejected its own block template: %v", err)
		} else if err != nil {
			t.Fatalf("ValidateAndInsertBlock: %+v", err)
		}
	})
}
