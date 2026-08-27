package consensusstatemanager

import (
	"github.com/HoosatNetwork/HTND/domain/consensus/database"
	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/infrastructure/logger"
	"github.com/pkg/errors"
)

func (csm *consensusStateManager) updateVirtual(stagingArea *model.StagingArea, newBlockHash *externalapi.DomainHash,
	tips []*externalapi.DomainHash,
) (*externalapi.SelectedChainPath, externalapi.UTXODiff, error) {
	onEnd := logger.LogAndMeasureExecutionTime(log, "updateVirtual")
	defer onEnd()

	log.Debugf("updateVirtual start for block %s", newBlockHash)

	log.Debugf("Saving a reference to the GHOSTDAG data of the old virtual")
	var oldVirtualSelectedParent *externalapi.DomainHash
	if !newBlockHash.Equal(csm.genesisHash) {
		oldVirtualGHOSTDAGData, err := csm.ghostdagDataStore.Get(csm.databaseContext, stagingArea, model.VirtualBlockHash, false)
		if database.IsNotFoundError(err) {
			log.Infof("updateVirtual failed to retrieve with %s\n", model.VirtualBlockHash)
			return nil, nil, err
		}
		if err != nil {
			return nil, nil, err
		}
		oldVirtualSelectedParent = oldVirtualGHOSTDAGData.SelectedParent()
	}

	log.Debugf("Picking virtual parents from tips len: %d", len(tips))
	virtualParents, err := csm.pickVirtualParents(stagingArea, tips)
	if err != nil {
		return nil, nil, err
	}
	log.Debugf("Picked virtual parents: %s", virtualParents)

	virtualUTXODiff, err := csm.updateVirtualWithParents(stagingArea, virtualParents)
	if err != nil {
		return nil, nil, err
	}

	log.Debugf("Calculating selected parent chain changes")
	var selectedParentChainChanges *externalapi.SelectedChainPath
	if !newBlockHash.Equal(csm.genesisHash) {
		newVirtualGHOSTDAGData, err := csm.ghostdagDataStore.Get(csm.databaseContext, stagingArea, model.VirtualBlockHash, false)
		if err != nil {
			return nil, nil, err
		}
		newVirtualSelectedParent := newVirtualGHOSTDAGData.SelectedParent()
		selectedParentChainChanges, err = csm.dagTraversalManager.
			CalculateChainPath(stagingArea, oldVirtualSelectedParent, newVirtualSelectedParent)
		if err != nil {
			return nil, nil, err
		}
		log.Debugf("Selected parent chain changes: %d blocks were removed and %d blocks were added",
			len(selectedParentChainChanges.Removed), len(selectedParentChainChanges.Added))
	}

	return selectedParentChainChanges, virtualUTXODiff, nil
}

func (csm *consensusStateManager) updateVirtualWithParents(
	stagingArea *model.StagingArea, virtualParents []*externalapi.DomainHash,
) (externalapi.UTXODiff, error) {
	err := csm.dagTopologyManager.SetParents(stagingArea, model.VirtualBlockHash, virtualParents)
	if err != nil {
		return nil, err
	}
	log.Debugf("Set new parents for the virtual block hash")

	err = csm.ghostdagManager.GHOSTDAG(stagingArea, model.VirtualBlockHash)
	if err != nil {
		return nil, err
	}

	// This is needed for `csm.CalculatePastUTXOAndAcceptanceData`
	_, err = csm.difficultyManager.StageDAADataAndReturnRequiredDifficulty(stagingArea, model.VirtualBlockHash, false)
	if err != nil {
		return nil, err
	}

	log.Debugf("Calculating past UTXO, acceptance data, and multiset for the new virtual block")
	virtualUTXODiff, virtualAcceptanceData, virtualMultiset, err := csm.CalculatePastUTXOAndAcceptanceData(stagingArea, model.VirtualBlockHash)
	if err != nil {
		return nil, err
	}

	log.Debugf("Calculated the past UTXO of the new virtual. "+
		"Diff toAdd length: %d, toRemove length: %d",
		virtualUTXODiff.ToAdd().Len(), virtualUTXODiff.ToRemove().Len())

	csm.acceptanceDataStore.Stage(stagingArea, model.VirtualBlockHash, virtualAcceptanceData)
	csm.multisetStore.Stage(stagingArea, model.VirtualBlockHash, virtualMultiset)
	csm.consensusStateStore.StageVirtualUTXODiff(stagingArea, virtualUTXODiff)

	log.Debugf("Updating the selected tip's utxo-diff")
	err = csm.updateSelectedTipUTXODiff(stagingArea, virtualUTXODiff)
	if err != nil {
		return nil, err
	}

	return virtualUTXODiff, nil
}

func (csm *consensusStateManager) updateSelectedTipUTXODiff(
	stagingArea *model.StagingArea, virtualUTXODiff externalapi.UTXODiff,
) error {
	onEnd := logger.LogAndMeasureExecutionTime(log, "updateSelectedTipUTXODiff")
	defer onEnd()

	selectedTip, err := csm.virtualSelectedParent(stagingArea)
	if err != nil {
		return err
	}

	log.Debugf("Calculating new UTXO diff for virtual diff parent %s", selectedTip)
	selectedTipUTXODiff, err := csm.utxoDiffStore.UTXODiff(csm.databaseContext, stagingArea, selectedTip)
	if err != nil {
		return err
	}
	newDiff, err := virtualUTXODiff.DiffFrom(selectedTipUTXODiff)
	if err != nil {
		// virtualUTXODiff and selectedTipUTXODiff were reconstructed by independently walking two
		// branches (virtual's new state vs. the previous selected tip), so - same as
		// resolveSingleBlockStatus's isResolveTip branch - they can disagree on the BlockDAAScore of
		// an outpoint they both otherwise agree on. Reconcile before diffing, exactly like that
		// existing tip-transition logic does, instead of masking the failure: virtualUTXODiff is
		// becoming canonical here, so its own reconstruction wins the disagreement.
		//
		// [UTXO-DEBUG] This used to fall back to staging virtualUTXODiff directly, with a nil
		// diffChild, as if it were selectedTip's own small diff. virtualUTXODiff is the FULL,
		// absolute diff (genesis/accumulation-start through virtual), not a delta relative to
		// selectedTip - substituting it here silently corrupted selectedTip's persisted diff-chain
		// entry every time this fallback fired, on every affected reorg, since this runs on every
		// new block (far more often than pruning-point advancement). That fallback is the confirmed
		// source of the drift traced this session: it reached even consensusStateStore's live
		// virtual UTXO table, not just pruning-point snapshots.
		log.Warnf("[UTXO-DEBUG] DiffFrom failed in updateSelectedTipUTXODiff for selected tip %s (err: %v) - "+
			"reconciling against virtual's own reconstruction instead of masking it.", selectedTip, err)
		reconciledSelectedTipUTXODiff, reconcileErr := reconcileWinningBranchUTXO(virtualUTXODiff, selectedTipUTXODiff)
		if reconcileErr != nil {
			return errors.Wrapf(reconcileErr, "updateSelectedTipUTXODiff: failed to reconcile selected tip "+
				"%s against virtual after DiffFrom failed (original DiffFrom error: %s)", selectedTip, err)
		}
		newDiff, err = virtualUTXODiff.DiffFrom(reconciledSelectedTipUTXODiff)
		if err != nil {
			return errors.Wrapf(err, "updateSelectedTipUTXODiff: DiffFrom still failed for selected tip %s "+
				"after reconciliation - this is a genuine conflict (not just a BlockDAAScore mismatch), "+
				"not something safe to paper over", selectedTip)
		}
	}

	log.Debugf("Staging new UTXO diff for virtual diff parent %s", selectedTip)
	csm.stageDiff(stagingArea, selectedTip, newDiff, nil)

	return nil
}
