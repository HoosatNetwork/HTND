package consensusstatemanager

import (
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/database"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/hashset"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/utxo"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/logger"
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

	// Captured before pickVirtualParents: bounded-merge GHOSTDAG writes virtual's parents as it
	// checks them, so a read afterwards is no longer the pre-update set.
	previousVirtualParents, previousParentsErr := csm.dagTopologyManager.Parents(stagingArea, model.VirtualBlockHash)
	if previousParentsErr != nil && !database.IsNotFoundError(previousParentsErr) {
		return nil, nil, previousParentsErr
	}

	log.Debugf("Picking virtual parents from tips len: %d", len(tips))
	virtualParents, err := csm.pickVirtualParents(stagingArea, tips)
	if err != nil {
		return nil, nil, err
	}
	log.Debugf("Picked virtual parents: %s", virtualParents)

	// Virtual's GHOSTDAG data, DAA window, acceptance data, multiset and UTXO diff are a function of
	// its parent set. GHOSTDAG sorts the merge set, so parent order does not enter the result. A new
	// tip that is not selected — the common case while syncing bodies that are already in the
	// selected parent's past — leaves that set unchanged, and rebuilding it is the same state.
	if previousParentsErr == nil && sameParentSet(previousVirtualParents, virtualParents) {
		log.Debugf("Virtual parents unchanged (%d), skipping GHOSTDAG and UTXO rebuild", len(virtualParents))
		return &externalapi.SelectedChainPath{}, utxo.NewUTXODiff(), nil
	}

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

// sameParentSet reports whether a and b contain the same blocks, regardless of order.
func sameParentSet(a, b []*externalapi.DomainHash) bool {
	if len(a) != len(b) {
		return false
	}
	set := hashset.New()
	for _, hash := range a {
		set.Add(hash)
	}
	for _, hash := range b {
		if !set.Contains(hash) {
			return false
		}
	}
	return true
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
		// Both diffs are relative to the same table, so DiffFrom represents a coin whose stamp differs
		// between them as a removal plus an addition. A failure here is a genuine conflict. It used to
		// be papered over by overwriting the selected tip's stamps with virtual's, which corrupted the
		// selected tip's stored diff instead of surfacing the problem.
		return errors.Wrapf(err, "updateSelectedTipUTXODiff: failed to diff virtual against selected tip %s", selectedTip)
	}

	log.Debugf("Staging new UTXO diff for virtual diff parent %s", selectedTip)
	csm.stageDiff(stagingArea, selectedTip, newDiff, nil)

	return nil
}
