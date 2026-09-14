package blockvalidator

import (
	"math/big"
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/ruleerrors"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/blockheader"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/constants"
	"github.com/pkg/errors"
)

// presetMassValidator leaves the mass a test stored on each transaction in place.
type presetMassValidator struct{ model.TransactionValidator }

func (presetMassValidator) PopulateMass(*externalapi.DomainTransaction) {}

// TestBlockMassLimitFollowsTheBlocksOwnVersion pins that a block's mass limit is the one for the block's own version.
// It was indexed by the process-global block version, so a restarted node (global 1) rejected valid heavy blocks of a
// later version, and a node whose global had advanced accepted blocks over their own version's limit.
func TestBlockMassLimitFollowsTheBlocksOwnVersion(t *testing.T) {
	defer constants.ForceSetBlockVersion(1)

	v := &blockValidator{
		maxBlockMass:         []uint64{500_000, 500_000, 500_000, 500_000, 1_000_000},
		transactionValidator: presetMassValidator{},
	}
	blockOfVersion := func(version uint16) *externalapi.DomainBlock {
		header := blockheader.NewImmutableBlockHeader(version, nil, &externalapi.DomainHash{}, &externalapi.DomainHash{},
			&externalapi.DomainHash{}, 0, 0, 0, 0, 0, big.NewInt(0), &externalapi.DomainHash{})
		transaction := &externalapi.DomainTransaction{}
		transaction.StoreMass(700_000)
		return &externalapi.DomainBlock{Header: header, Transactions: []*externalapi.DomainTransaction{transaction}}
	}

	for _, globalVersion := range []uint{1, 9} {
		constants.ForceSetBlockVersion(globalVersion)
		if err := v.checkBlockMass(blockOfVersion(5)); err != nil {
			t.Errorf("global version %d: a 700k-mass version-5 block (limit 1M) was rejected: %v", globalVersion, err)
		}
		if err := v.checkBlockMass(blockOfVersion(1)); !errors.Is(err, ruleerrors.ErrBlockMassTooHigh) {
			t.Errorf("global version %d: a 700k-mass version-1 block (limit 500k) was not rejected: %v", globalVersion, err)
		}
	}
}
