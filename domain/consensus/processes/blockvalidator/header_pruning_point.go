package blockvalidator

import (
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/ruleerrors"
	"github.com/pkg/errors"
)

// validateHeaderPruningPoint checks that the header's pruning point is the one this node expects for
// it from its selected parent - the same value the block builder writes into a template. HTN-001:
// without it nothing cross-checks the pruning point a header claims, which is half of why two nodes
// with identical blocks could choose different pruning points.
func (v *blockValidator) validateHeaderPruningPoint(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash,
	header externalapi.BlockHeader,
) error {
	if blockHash.Equal(v.genesisHash) {
		return nil
	}

	expectedPruningPoint, err := v.pruningManager.ExpectedHeaderPruningPoint(stagingArea, blockHash)
	if err != nil {
		return err
	}

	if !header.PruningPoint().Equal(expectedPruningPoint) {
		return errors.Wrapf(ruleerrors.ErrUnexpectedPruningPoint, "block pruning point of %s is not the expected hash of %s",
			header.PruningPoint(), expectedPruningPoint)
	}
	return nil
}
