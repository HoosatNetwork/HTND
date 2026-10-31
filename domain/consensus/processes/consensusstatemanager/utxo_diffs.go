package consensusstatemanager

import (
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/hashset"
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

// stageSelectedTipDiffRelativeToVirtual gives selectedTip, the block virtual is about to have as its selected parent,
// the UTXO diff virtual's selected parent must have: relative to virtual's UTXO set, with no diff child.
// updateSelectedTipUTXODiff reads the selected tip's diff as relative to virtual, and every other block's past is
// restored by composing diffs along diff children up to that one root.
//
// A block that becomes the selected tip through resolveSingleBlockStatus as a new selected tip already has such a diff,
// and nothing is staged. Any other block still has a diff relative to its diff child: a disqualified resolve processing
// point (ResolveBlockStatus gives every block of a disqualified chain a diff relative to its selected parent, and
// ReverseUTXODiffs only runs for a valid one), a tip that was already UTXO-valid, a valid block whose resolve tip was
// disqualified. Reading such a diff as virtual-relative put a wrong past into virtual's UTXO set, and the previous root
// kept no diff child, so every block whose diff chain ended there restored a wrong past from then on.
//
// The current root - the block whose diff is relative to virtual's UTXO set, normally the previous selected parent - is
// found by following diff children from selectedTip, as restorePastUTXO does, and is re-pointed to selectedTip, as
// resolveSingleBlockStatus does for a new valid selected tip. Both pasts are restored before anything is staged, so
// every block's restored past is unchanged.
func (csm *consensusStateManager) stageSelectedTipDiffRelativeToVirtual(stagingArea *model.StagingArea,
	selectedTip *externalapi.DomainHash,
) error {
	root, err := csm.utxoDiffStore.UTXODiffChild(csm.databaseContext, stagingArea, selectedTip)
	if err != nil {
		return err
	}
	if root == nil {
		return nil
	}
	visited := hashset.New()
	visited.Add(selectedTip)
	for {
		if visited.Contains(root) {
			return errors.Errorf("UTXO diff child chain from %s loops at %s", selectedTip, root)
		}
		visited.Add(root)
		child, err := csm.utxoDiffStore.UTXODiffChild(csm.databaseContext, stagingArea, root)
		if err != nil {
			return err
		}
		if child == nil {
			break
		}
		root = child
	}
	// The root must hold a stored diff; restorePastUTXO tolerates a missing one, which would hide a broken chain here.
	if _, err := csm.utxoDiffStore.UTXODiff(csm.databaseContext, stagingArea, root); err != nil {
		return errors.Wrapf(err, "UTXO diff root %s of the new selected tip %s has no stored UTXO diff", root, selectedTip)
	}

	selectedTipPastUTXO, err := csm.restorePastUTXO(stagingArea, selectedTip)
	if err != nil {
		return err
	}
	rootPastUTXO, err := csm.restorePastUTXO(stagingArea, root)
	if err != nil {
		return err
	}
	rootUTXODiff, err := selectedTipPastUTXO.DiffFrom(rootPastUTXO)
	if err != nil {
		return errors.Wrapf(err, "failed to diff the new selected tip %s against the previous UTXO diff root %s",
			selectedTip, root)
	}
	log.Debugf("Re-pointing the UTXO diff root %s to the new selected tip %s", root, selectedTip)
	csm.stageDiff(stagingArea, root, rootUTXODiff, selectedTip)
	csm.stageDiff(stagingArea, selectedTip, selectedTipPastUTXO, nil)
	return nil
}
