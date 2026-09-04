package consensusstatemanager

import (
	"sort"

	"github.com/HoosatNetwork/HTND/domain/consensus/database"
	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/infrastructure/logger"
	"github.com/HoosatNetwork/HTND/util/staging"
	"github.com/pkg/errors"
)

// tipsInDecreasingDAGKnightOrder returns the current DAG tips in decreasing DAGKnight ordering.
// This means that the first tip in the resulting list would be the DAGKnight selected tip, and if removed from the list,
// the second tip would be the next in the ordering, and so on.
func (csm *consensusStateManager) tipsInDecreasingDAGKnightOrder(stagingArea *model.StagingArea) ([]*externalapi.DomainHash, error) {
	tips, err := csm.consensusStateStore.Tips(stagingArea, csm.databaseContext)
	if err != nil {
		return nil, err
	}

	// Use DAGKnight OrderDAG to get the ordering of the tips
	_, ordering, err := csm.ghostdagManager.OrderDAG(stagingArea, tips)
	if err != nil {
		return nil, err
	}

	// Create a map from hash to its position in the ordering (lower index = higher priority)
	orderMap := make(map[externalapi.DomainHash]int)
	for i, hash := range ordering {
		orderMap[*hash] = i
	}

	// Sort tips by their position in the ordering (ascending order index)
	sort.Slice(tips, func(i, j int) bool {
		return orderMap[*tips[i]] < orderMap[*tips[j]]
	})

	return tips, nil
}

func (csm *consensusStateManager) tipsInDecreasingGHOSTDAGParentSelectionOrder(stagingArea *model.StagingArea) ([]*externalapi.DomainHash, error) {
	tips, err := csm.consensusStateStore.Tips(stagingArea, csm.databaseContext)
	if err != nil {
		return nil, err
	}

	var sortErr error
	sort.Slice(tips, func(i, j int) bool {
		selectedParent, err := csm.ghostdagManager.ChooseSelectedParent(stagingArea, tips[i], tips[j])
		if err != nil {
			sortErr = err
			return false
		}

		return selectedParent.Equal(tips[i])
	})
	if sortErr != nil {
		return nil, sortErr
	}
	return tips, nil
}

func (csm *consensusStateManager) findNextPendingTip(stagingArea *model.StagingArea) (*externalapi.DomainHash, externalapi.BlockStatus, error) {
	var orderedTips []*externalapi.DomainHash
	var err error
	// DAGKnight TODO: decide DAA Score for hard fork
	if constants.GetBlockVersion() >= 6 {
		orderedTips, err = csm.tipsInDecreasingDAGKnightOrder(stagingArea)
	} else {
		orderedTips, err = csm.tipsInDecreasingGHOSTDAGParentSelectionOrder(stagingArea)
	}
	// log.Infof("Number of tips %d", len(orderedTips))
	if err != nil {
		return nil, externalapi.StatusInvalid, err
	}

	for _, tip := range orderedTips {
		log.Debugf("Resolving tip %s", tip)
		isViolatingFinality, shouldNotify, err := csm.isViolatingFinality(stagingArea, tip)
		if err != nil {
			return nil, externalapi.StatusInvalid, err
		}

		if isViolatingFinality {
			log.Infof("Tip %s is violating finality", tip)
			if shouldNotify {
				// TODO: Send finality conflict notification
				log.Warnf("Skipping %s tip resolution because it violates finality", tip)
			}
			continue
		}

		status, err := csm.blockStatusStore.Get(csm.databaseContext, stagingArea, tip)
		log.Debugf("Tip status %s", status)
		if err != nil {
			return nil, externalapi.StatusInvalid, err
		}
		if status == externalapi.StatusUTXOValid || status == externalapi.StatusUTXOPendingVerification {
			return tip, status, nil
		}
	}

	// If no pending tip found among DAG tips, check the headers selected parent chain.
	// This handles the case where blocks in the selected chain are not yet tips
	// (e.g., during IBD when bodies are being synced and blocks have StatusHeaderOnly).
	log.Debugf("No pending tip found among DAG tips, checking headers selected parent chain")
	headerSelectedTip, err := csm.headersSelectedTipStore.HeadersSelectedTip(csm.databaseContext, stagingArea)
	if err != nil {
		log.Warnf("Failed to get headers selected tip: %v", err)
	} else if headerSelectedTip != nil {
		currentHash := headerSelectedTip
		for {
			status, err := csm.blockStatusStore.Get(csm.databaseContext, stagingArea, currentHash)
			if database.IsNotFoundError(err) {
				log.Debugf("Block %s not found in status store, walking up selected parent chain", currentHash)
				break
			}
			if err != nil {
				log.Warnf("Failed to get status for block %s: %v", currentHash, err)
				break
			}
			if status == externalapi.StatusUTXOValid || status == externalapi.StatusUTXOPendingVerification {
				log.Debugf("Found pending block %s in selected parent chain with status %s", currentHash, status)
				return currentHash, status, nil
			}
			// Continue walking up the selected parent chain
			ghostdagData, err := csm.ghostdagDataStore.Get(csm.databaseContext, stagingArea, currentHash, false)
			if database.IsNotFoundError(err) {
				log.Debugf("GHOSTDAG data not found for %s, walking up", currentHash)
				break
			}
			if err != nil {
				log.Warnf("Failed to get GHOSTDAG data for block %s: %v", currentHash, err)
				break
			}
			nextHash := ghostdagData.SelectedParent()
			if nextHash == nil {
				break
			}
			if nextHash.Equal(csm.genesisHash) {
				// Genesis block should be StatusUTXOValid
				return nextHash, externalapi.StatusUTXOValid, nil
			}
			currentHash = nextHash
		}
	}

	log.Infof("None of the tips were valid or pending, so printing all the statuses")
	for _, tip := range orderedTips {
		status, err := csm.blockStatusStore.Get(csm.databaseContext, stagingArea, tip)
		if err != nil {
			log.Infof("Error happened fetching status: %s", err)
		}
		log.Infof("Status: %s", status)
	}

	return nil, externalapi.StatusInvalid, errors.Errorf(
		"no pending tip: all %d tips are disqualified/invalid", len(orderedTips))
}

// getGHOSTDAGLowerTips returns the set of tips which are lower in GHOSTDAG parent selection order than `pendingTip`. i.e.,
// they can be added to virtual parents but `pendingTip` will remain the virtual selected parent
func (csm *consensusStateManager) getGHOSTDAGLowerTips(stagingArea *model.StagingArea, pendingTip *externalapi.DomainHash) ([]*externalapi.DomainHash, error) {
	tips, err := csm.consensusStateStore.Tips(stagingArea, csm.databaseContext)
	if err != nil {
		return nil, err
	}

	lowerTips := []*externalapi.DomainHash{pendingTip}
	for _, tip := range tips {
		if tip.Equal(pendingTip) {
			continue
		}
		selectedParent, err := csm.ghostdagManager.ChooseSelectedParent(stagingArea, tip, pendingTip)
		if err != nil {
			return nil, err
		}
		if selectedParent.Equal(pendingTip) {
			lowerTips = append(lowerTips, tip)
		}
	}
	return lowerTips, nil
}

// RecomputeVirtual re-picks virtual's parents from the current tips and re-colors virtual from
// scratch, then commits.
//
// Virtual is only ever re-colored as a side effect of a block arriving (AddBlock -> updateVirtual) or
// of an IBD run finishing (resolveVirtual). A node whose DAG has stopped moving therefore keeps
// whatever GHOSTDAG data virtual was last stored with, forever - which is exactly the situation where
// that data is most likely to be the thing that is wrong, and where nothing will ever fix it on its
// own. This gives that repair an explicit trigger.
//
// It deliberately goes through pickVirtualParents rather than reusing virtual's existing parents:
// re-coloring alone would leave a parent set that was chosen under the bad coloring in place, and
// boundedMergeBreakingParents is precisely the filter that needs to run again over the corrected one.
func (csm *consensusStateManager) RecomputeVirtual() error {
	onEnd := logger.LogAndMeasureExecutionTime(log, "csm.RecomputeVirtual")
	defer onEnd()

	readStagingArea := model.NewStagingArea()
	tips, err := csm.consensusStateStore.Tips(readStagingArea, csm.databaseContext)
	if err != nil {
		return err
	}
	log.Infof("Recomputing virtual from %d tips", len(tips))

	virtualParents, err := csm.pickVirtualParents(readStagingArea, tips)
	if err != nil {
		return err
	}
	log.Infof("Recomputed virtual parents: %d of %d tips selected", len(virtualParents), len(tips))

	updateVirtualStagingArea := model.NewStagingArea()
	_, err = csm.updateVirtualWithParents(updateVirtualStagingArea, virtualParents)
	if err != nil {
		return err
	}

	return staging.CommitAllChanges(csm.databaseContext, updateVirtualStagingArea)
}

func (csm *consensusStateManager) ResolveVirtual(maxBlocksToResolve uint64) (*externalapi.VirtualChangeSet, bool, error) {
	onEnd := logger.LogAndMeasureExecutionTime(log, "csm.ResolveVirtual")
	defer onEnd()

	// We use a read-only staging area for some read-only actions, to avoid
	// confusion with the resolve/updateVirtual staging areas below
	readStagingArea := model.NewStagingArea()

	log.Debugf("Finding next pending tip")
	pendingTip, pendingTipStatus, err := csm.findNextPendingTip(readStagingArea)
	if err != nil {
		return nil, false, err
	}

	if pendingTip == nil {
		log.Warnf("None of the DAG tips are valid, because of %s", pendingTipStatus)
		return nil, false, nil
	}
	log.Debugf("Previous pending tip %s", pendingTip)

	log.Debugf("Finding virtual selected parent")
	previousVirtualSelectedParent, err := csm.virtualSelectedParent(readStagingArea)
	if err != nil {
		return nil, false, err
	}
	log.Debugf("Previous virtual selected parent %s", previousVirtualSelectedParent)

	if pendingTipStatus == externalapi.StatusUTXOValid && previousVirtualSelectedParent.Equal(pendingTip) {
		// Check if headers selected tip is beyond the pending tip.
		// If so, there are header-only blocks in the selected chain that need resolution.
		headerSelectedTip, err := csm.headersSelectedTipStore.HeadersSelectedTip(csm.databaseContext, readStagingArea)
		if err != nil {
			log.Warnf("Failed to check headers selected tip for early exit: %v", err)
			// Continue with resolution to be safe
		} else if headerSelectedTip != nil && !headerSelectedTip.Equal(pendingTip) {
			// Headers selected tip is different from pending tip - blocks need resolution
			log.Debugf("Headers selected tip %s differs from pending tip %s, continuing resolution", headerSelectedTip, pendingTip)
		} else {
			// No need to resolve - virtual is already at the headers selected tip
			return nil, true, nil
		}
	}
	log.Debugf("Pending tip was UTXO Valid and they were same, but headers tip differs or resolution needed")

	// Resolve a chunk from the pending chain
	resolveStagingArea := model.NewStagingArea()
	unverifiedBlocks, err := csm.getUnverifiedChainBlocks(resolveStagingArea, pendingTip)
	if err != nil {
		return nil, false, err
	}

	// Initially set the resolve processing point to the pending tip
	processingPoint := pendingTip
	log.Debugf("Processing point %s", processingPoint)

	// Too many blocks to verify, so we only process a chunk and return
	if maxBlocksToResolve != 0 && uint64(len(unverifiedBlocks)) > maxBlocksToResolve {
		processingPointIndex := uint64(len(unverifiedBlocks)) - maxBlocksToResolve
		processingPoint = unverifiedBlocks[processingPointIndex]
		isNewVirtualSelectedParent, err := csm.isNewSelectedTip(readStagingArea, processingPoint, previousVirtualSelectedParent)
		if err != nil {
			return nil, false, err
		}

		// We must find a processing point which wins previous virtual selected parent
		// even if we process more than `maxBlocksToResolve` for that.
		// Otherwise, internal UTXO diff logic gets all messed up
		for !isNewVirtualSelectedParent {
			if processingPointIndex == 0 {
				// If we've reached the pending tip and it still doesn't overcome the previous
				// virtual selected parent, this could happen in nearly synced scenarios where
				// GHOSTDAG data isn't fully consistent. Log a warning and process from the pending tip.
				log.Warnf("Pending tip %s does not overcome previous selected parent %s. Processing entire unverified chain from pending tip.", pendingTip, previousVirtualSelectedParent)
				processingPoint = pendingTip
				break
			}
			processingPointIndex--
			processingPoint = unverifiedBlocks[processingPointIndex]
			isNewVirtualSelectedParent, err = csm.isNewSelectedTip(readStagingArea, processingPoint, previousVirtualSelectedParent)
			if err != nil {
				return nil, false, err
			}
		}
		log.Debugf("Has more than %d blocks to resolve. Setting the resolve processing point to %s", maxBlocksToResolve, processingPoint)
	}

	// Keep the whole resolve chunk in a single staging area so late-IBD resolution
	// avoids repeated database commits for every intermediate block. This preserves
	// the same UTXO diff and status semantics while reducing I/O overhead.
	processingPointStatus, reversalData, err := csm.ResolveBlockStatus(
		resolveStagingArea, processingPoint, false)
	if err != nil {
		return nil, false, err
	}

	err = staging.CommitAllChanges(csm.databaseContext, resolveStagingArea)
	if err != nil {
		return nil, false, err
	}

	if processingPointStatus == externalapi.StatusUTXOValid && reversalData != nil {
		err = csm.ReverseUTXODiffs(processingPoint, reversalData)
		if err != nil {
			return nil, false, err
		}
	}

	isActualTip := processingPoint.Equal(pendingTip)
	isCompletelyResolved := isActualTip && processingPointStatus == externalapi.StatusUTXOValid

	updateVirtualStagingArea := model.NewStagingArea()

	virtualParents := []*externalapi.DomainHash{processingPoint}
	// If `isCompletelyResolved`, set virtual correctly with all tips which have less blue work than pending
	if isCompletelyResolved {
		lowerTips, err := csm.getGHOSTDAGLowerTips(readStagingArea, pendingTip)
		if err != nil {
			return nil, false, err
		}
		log.Debugf("Picking virtual parents from relevant tips len: %d", len(lowerTips))

		virtualParents, err = csm.pickVirtualParents(readStagingArea, lowerTips)
		if err != nil {
			return nil, false, err
		}
		log.Debugf("Picked virtual parents: %s", virtualParents)
	}
	virtualUTXODiff, err := csm.updateVirtualWithParents(updateVirtualStagingArea, virtualParents)
	if err != nil {
		return nil, false, err
	}

	err = staging.CommitAllChanges(csm.databaseContext, updateVirtualStagingArea)
	if err != nil {
		return nil, false, err
	}

	selectedParentChainChanges, err := csm.dagTraversalManager.
		CalculateChainPath(updateVirtualStagingArea, previousVirtualSelectedParent, processingPoint)
	if err != nil {
		return nil, false, err
	}

	virtualParentsOutcome, err := csm.dagTopologyManager.Parents(updateVirtualStagingArea, model.VirtualBlockHash)
	if err != nil {
		return nil, false, err
	}

	// Add other stores if needed

	return &externalapi.VirtualChangeSet{
		VirtualSelectedParentChainChanges: selectedParentChainChanges,
		VirtualUTXODiff:                   virtualUTXODiff,
		VirtualParents:                    virtualParentsOutcome,
	}, isCompletelyResolved, nil
}
