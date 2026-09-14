package blocktemplatebuilder

import (
	"math"
	"testing"

	consensusexternalapi "github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/subnetworks"
	"github.com/HoosatNetwork/HTND/domain/consensusreference"
)

type virtualDAAScoreConsensus struct {
	consensusexternalapi.Consensus
	virtualDAAScore uint64
}

func (c virtualDAAScoreConsensus) GetVirtualDAAScore() (uint64, error) { return c.virtualDAAScore, nil }

// TestTemplateMassFollowsTheNextBlocksVersion pins that a block template is filled only up to the mass limit of the
// version the template block will have. The cap was indexed by the process-global block version, while validation
// applies the block's own version's limit, so a node whose global was ahead of the chain - raised by a relayed header
// - filled a version-1 template past its 500k limit and rejected its own block.
func TestTemplateMassFollowsTheNextBlocksVersion(t *testing.T) {
	defer constants.ForceSetBlockVersion(1)

	var consensus consensusexternalapi.Consensus = virtualDAAScoreConsensus{virtualDAAScore: 1_000}
	consensusPointer := &consensus
	btb := New(consensusreference.NewConsensusReference(&consensusPointer), nil,
		[]uint64{500_000, 500_000, 500_000, 500_000, 1_000_000, 1_000_000, 1_000_000, 1_000_000, 1_000_000}, 0,
		[]uint64{math.MaxUint64}).(*blockTemplateBuilder)

	newCandidates := func() []*candidateTx {
		candidates := make([]*candidateTx, 3)
		for i := range candidates {
			transaction := &consensusexternalapi.DomainTransaction{
				SubnetworkID: subnetworks.SubnetworkIDNative,
				Payload:      []byte{byte(i)},
			}
			transaction.StoreMass(300_000)
			transaction.StoreFee(1_000)
			candidates[i] = &candidateTx{DomainTransaction: transaction, txValue: 1}
		}
		return candidates
	}

	for _, globalVersion := range []uint{1, 9} {
		constants.ForceSetBlockVersion(globalVersion)
		selected := btb.selectTransactions(newCandidates())
		if selected.totalMass > 500_000 {
			t.Errorf("global version %d: a version-1 template was filled to mass %d, above its 500k limit",
				globalVersion, selected.totalMass)
		}
	}
}
