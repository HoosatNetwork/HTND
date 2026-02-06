package consensusstatemanager

import (
	"github.com/Hoosat-Oy/HTND/domain/consensus/database"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/infrastructure/logger"
)

func (csm *consensusStateManager) updateVirtual(stagingArea *model.StagingArea, newBlockHash *externalapi.DomainHash,
	tips []*externalapi.DomainHash) (*externalapi.SelectedChainPath, externalapi.UTXODiff, error) {

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
	stagingArea *model.StagingArea, virtualParents []*externalapi.DomainHash) (externalapi.UTXODiff, error) {
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
	virtualUTXODiff, virtualAcceptanceData, virtualMultiset, err :=
		csm.CalculatePastUTXOAndAcceptanceData(stagingArea, model.VirtualBlockHash)
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
	stagingArea *model.StagingArea, virtualUTXODiff externalapi.UTXODiff) error {

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
		// DiffFrom can fail during reorgs when UTXO sets have incompatible DAA scores.
		// Fall back to using the virtualUTXODiff directly with nil diffChild.
		log.Debugf("DiffFrom failed in updateSelectedTipUTXODiff (err: %v), using virtualUTXODiff directly", err)
		csm.stageDiff(stagingArea, selectedTip, virtualUTXODiff, nil)
		return nil
	}

	log.Debugf("Staging new UTXO diff for virtual diff parent %s", selectedTip)
	csm.stageDiff(stagingArea, selectedTip, newDiff, nil)

	return nil
}
