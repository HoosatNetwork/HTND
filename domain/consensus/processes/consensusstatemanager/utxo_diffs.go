package consensusstatemanager

import (
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/pkg/errors"
)

func (csm *consensusStateManager) stageDiff(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash,
	utxoDiff externalapi.UTXODiff, utxoDiffChild *externalapi.DomainHash,
) {
	log.Tracef("stageDiff start for block %s", blockHash)
	defer log.Tracef("stageDiff end for block %s", blockHash)

	log.Debugf("Staging block %s as the diff child of %s", utxoDiffChild, blockHash)
	csm.utxoDiffStore.Stage(stagingArea, blockHash, utxoDiff, utxoDiffChild)
}

// stageDisqualifiedProcessingPointAsSelectedTip gives a disqualified resolve processing point the UTXO diff virtual's
// selected parent must have: relative to virtual's UTXO set, with no diff child. ResolveBlockStatus leaves every block
// of a disqualified chain, the resolve tip included, with a diff relative to its selected parent, and ReverseUTXODiffs
// only runs for a valid one. updateSelectedTipUTXODiff reads the selected tip's diff as relative to virtual, so the
// processing point's past lost the changes of every block of the chunk but its own. The next chunk started from that
// past and committed it into virtual's UTXO set, and the difference carried into every chunk after it, growing the
// diffs each chunk had to process.
//
// The previous virtual selected parent's diff stops being relative to virtual once virtual moves, so it is re-pointed
// to the processing point, as resolveSingleBlockStatus does for a new valid selected tip.
func (csm *consensusStateManager) stageDisqualifiedProcessingPointAsSelectedTip(stagingArea *model.StagingArea,
	processingPoint, previousVirtualSelectedParent *externalapi.DomainHash,
) error {
	processingPointPastUTXO, err := csm.restorePastUTXO(stagingArea, processingPoint)
	if err != nil {
		return err
	}

	if !previousVirtualSelectedParent.Equal(processingPoint) {
		previousVirtualSelectedParentPastUTXO, err := csm.restorePastUTXO(stagingArea, previousVirtualSelectedParent)
		if err != nil {
			return err
		}
		previousVirtualSelectedParentUTXODiff, err := processingPointPastUTXO.DiffFrom(previousVirtualSelectedParentPastUTXO)
		if err != nil {
			return errors.Wrapf(err, "failed to diff disqualified processing point %s against the previous virtual "+
				"selected parent %s", processingPoint, previousVirtualSelectedParent)
		}
		csm.stageDiff(stagingArea, previousVirtualSelectedParent, previousVirtualSelectedParentUTXODiff, processingPoint)
	}

	csm.stageDiff(stagingArea, processingPoint, processingPointPastUTXO, nil)
	return nil
}
