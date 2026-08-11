package consensusstatemanager

import (
	"fmt"

	"github.com/HoosatNetwork/HTND/util/staging"

	"github.com/HoosatNetwork/HTND/domain/consensus/database"
	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/ruleerrors"
	"github.com/HoosatNetwork/HTND/infrastructure/logger"
	"github.com/pkg/errors"
)

func (csm *consensusStateManager) ResolveBlockStatus(
	stagingArea *model.StagingArea,
	blockHash *externalapi.DomainHash,
	useSeparateStagingAreaPerBlock bool,
) (externalapi.BlockStatus, *model.UTXODiffReversalData, error) {
	onEnd := logger.LogAndMeasureExecutionTime(log, fmt.Sprintf("resolveBlockStatus for %s", blockHash))
	defer onEnd()

	// ------------------------------------------------------------------
	// Check cache first
	// ------------------------------------------------------------------
	if cachedEntry, ok := csm.resolveBlockStatusCache.Get(blockHash); ok {
		log.Debugf("ResolveBlockStatus cache hit for %s", blockHash)
		return cachedEntry.status, cachedEntry.reversalData, nil
	}

	// ------------------------------------------------------------------
	// Early exit: blocks whose selected-parent chain does not contain the
	// pruning point cannot be fully resolved (would require pruned UTXO data).
	// ------------------------------------------------------------------
	pruningPoint, err := csm.pruningStore.PruningPoint(csm.databaseContext, stagingArea)
	if err != nil {
		// No pruning point yet (initial sync / tests) → resolve normally.
		log.Debugf("No pruning point exists yet, proceeding with normal resolution for %s", blockHash)
	} else if !csm.genesisHash.Equal(blockHash) {
		isInSelectedChain, err := csm.dagTopologyManager.IsInSelectedParentChainOf(
			stagingArea, pruningPoint, blockHash)
		if err != nil {
			return 0, nil, err
		}
		if !isInSelectedChain {
			log.Debugf("Block %s is not in the selected-parent chain of pruning point %s → StatusUTXOPendingVerification",
				blockHash, pruningPoint)
			// Cache the result
			csm.resolveBlockStatusCache.Add(blockHash, resolveBlockStatusCacheEntry{status: externalapi.StatusUTXOPendingVerification, reversalData: nil})
			return externalapi.StatusUTXOPendingVerification, nil, nil
		}
	}

	// ------------------------------------------------------------------
	// Collect the unresolved chain
	// ------------------------------------------------------------------
	log.Debugf("Collecting unresolved blocks in the selected-parent chain of %s", blockHash)
	unverifiedBlocks, err := csm.getUnverifiedChainBlocks(stagingArea, blockHash)
	if err != nil {
		return 0, nil, err
	}
	log.Debugf("Found %d unresolved blocks for %s", len(unverifiedBlocks), blockHash)

	// Already fully resolved → just return the stored status.
	if len(unverifiedBlocks) == 0 {
		status, err := csm.blockStatusStore.Get(csm.databaseContext, stagingArea, blockHash)
		if err != nil {
			if database.IsNotFoundError(err) {
				log.Infof("ResolveBlockStatus: status not found for already-resolved block %s", blockHash)
			}
			return 0, nil, err
		}
		log.Debugf("Block %s already has UTXO-verified status: %s", blockHash, status)
		// Cache the result
		csm.resolveBlockStatusCache.Add(blockHash, resolveBlockStatusCacheEntry{status: status, reversalData: nil})
		return status, nil, nil
	}

	// ------------------------------------------------------------------
	// Obtain the starting point (selected parent of the unresolved chain)
	// ------------------------------------------------------------------
	log.Debugf("Resolving selected-parent info for the chain of %s", blockHash)
	selectedParentHash, selectedParentStatus, selectedParentUTXOSet, err :=
		csm.selectedParentInfo(stagingArea, unverifiedBlocks)
	if err != nil {
		return 0, nil, err
	}
	log.Debugf("Selected parent of %s is %s with status %s",
		blockHash, selectedParentHash, selectedParentStatus)

	// ------------------------------------------------------------------
	// Walk the chain from past → present and resolve each block
	// ------------------------------------------------------------------
	var (
		blockStatus                    externalapi.BlockStatus
		previousBlockHash              = selectedParentHash
		previousBlockUTXOSet           = selectedParentUTXOSet
		oneBeforeLastResolvedBlockHash *externalapi.DomainHash
		oneBeforeLastResolvedBlockUTXO externalapi.UTXODiff
	)

	for i := len(unverifiedBlocks) - 1; i >= 0; i-- {
		unverifiedBlockHash := unverifiedBlocks[i]
		isResolveTip := i == 0

		// Optional per-block staging area (everything except the tip).
		stagingAreaForCurrentBlock := stagingArea
		if useSeparateStagingAreaPerBlock && !isResolveTip {
			stagingAreaForCurrentBlock = model.NewStagingArea()
		}

		if selectedParentStatus == externalapi.StatusDisqualifiedFromChain {
			// Special path: propagate disqualification while still producing
			// a continuous UTXO-diff chain (needed for later restorePastUTXO).
			blockStatus = externalapi.StatusDisqualifiedFromChain
		} else {
			// Normal path – remember the state just before the tip for later reversal.
			oneBeforeLastResolvedBlockUTXO = previousBlockUTXOSet
			oneBeforeLastResolvedBlockHash = previousBlockHash

			blockStatus, previousBlockUTXOSet, err = csm.resolveSingleBlockStatus(
				stagingAreaForCurrentBlock,
				unverifiedBlockHash,
				previousBlockHash,
				previousBlockUTXOSet,
				isResolveTip,
			)
			if err != nil {
				return 0, nil, err
			}
		}

		// Stage the resolved status and advance the “selected parent” for the next iteration.
		csm.blockStatusStore.Stage(stagingAreaForCurrentBlock, unverifiedBlockHash, blockStatus)
		selectedParentStatus = blockStatus

		log.Debugf("Block %s → %s  (%d/%d)",
			unverifiedBlockHash, blockStatus,
			len(unverifiedBlocks)-i, len(unverifiedBlocks))

		if useSeparateStagingAreaPerBlock && !isResolveTip {
			if err := staging.CommitAllChanges(csm.databaseContext, stagingAreaForCurrentBlock); err != nil {
				return 0, nil, err
			}
		}

		previousBlockHash = unverifiedBlockHash
	}

	// ------------------------------------------------------------------
	// Prepare reversal data (only when we produced a valid tip and the chain
	// was longer than one block). This lets the caller later shorten the
	// UTXODiffChild paths.
	// ------------------------------------------------------------------
	var reversalData *model.UTXODiffReversalData
	if blockStatus == externalapi.StatusUTXOValid && len(unverifiedBlocks) > 1 {
		log.Debugf("Preparing UTXODiff reversal data for the resolved chain of %s", blockHash)

		selectedParentUTXODiff, err := previousBlockUTXOSet.DiffFrom(oneBeforeLastResolvedBlockUTXO)
		if err != nil {
			return 0, nil, err
		}

		reversalData = &model.UTXODiffReversalData{
			SelectedParentHash:     oneBeforeLastResolvedBlockHash,
			SelectedParentUTXODiff: selectedParentUTXODiff,
		}
	}

	// Cache the result
	csm.resolveBlockStatusCache.Add(blockHash, resolveBlockStatusCacheEntry{status: blockStatus, reversalData: reversalData})
	return blockStatus, reversalData, nil
}

// selectedParentInfo returns the hash and status of the selectedParent of the last block in the unverifiedBlocks
// chain, in addition, if the status is UTXOValid, it return it's pastUTXOSet
func (csm *consensusStateManager) selectedParentInfo(
	stagingArea *model.StagingArea, unverifiedBlocks []*externalapi.DomainHash,
) (*externalapi.DomainHash, externalapi.BlockStatus, externalapi.UTXODiff, error) {
	log.Tracef("selectedParentInfo start")
	defer log.Tracef("selectedParentInfo end")

	lastUnverifiedBlock := unverifiedBlocks[len(unverifiedBlocks)-1]

	// Special-case genesis: it is always UTXO-valid by definition.
	if lastUnverifiedBlock.Equal(csm.genesisHash) {
		log.Debugf("most recent unverified block is genesis → status %s", externalapi.StatusUTXOValid)
		utxoDiff, err := csm.utxoDiffStore.UTXODiff(csm.databaseContext, stagingArea, lastUnverifiedBlock)
		if err != nil {
			return nil, 0, nil, err
		}
		return lastUnverifiedBlock, externalapi.StatusUTXOValid, utxoDiff, nil
	}

	lastUnverifiedBlockGHOSTDAGData, err := csm.ghostdagDataStore.Get(
		csm.databaseContext, stagingArea, lastUnverifiedBlock, false)
	if err != nil {
		if database.IsNotFoundError(err) {
			log.Infof("selectedParentInfo: GHOSTDAG data not found for %s", lastUnverifiedBlock)
		}
		return nil, 0, nil, err
	}

	selectedParent := lastUnverifiedBlockGHOSTDAGData.SelectedParent()

	selectedParentStatus, err := csm.blockStatusStore.Get(
		csm.databaseContext, stagingArea, selectedParent)
	if err != nil {
		if database.IsNotFoundError(err) {
			log.Infof("selectedParentInfo: status not found for selected parent %s", selectedParent)
		}
		return nil, 0, nil, err
	}

	// Only restore the (potentially expensive) past UTXO when the selected
	// parent is UTXO-valid is something other than StatusUTXOValid. For every other status
	// (header-only, etc.) we return early with a nil UTXODiff.
	if selectedParentStatus != externalapi.StatusUTXOValid {
		return selectedParent, selectedParentStatus, nil, nil
	}

	selectedParentUTXOSet, err := csm.restorePastUTXO(stagingArea, selectedParent)
	if err != nil {
		return nil, 0, nil, err
	}

	return selectedParent, selectedParentStatus, selectedParentUTXOSet, nil
}

func (csm *consensusStateManager) getUnverifiedChainBlocks(stagingArea *model.StagingArea,
	blockHash *externalapi.DomainHash,
) ([]*externalapi.DomainHash, error) {
	log.Tracef("getUnverifiedChainBlocks start for block %s", blockHash)
	defer log.Tracef("getUnverifiedChainBlocks end for block %s", blockHash)

	var unverifiedBlocks []*externalapi.DomainHash
	currentHash := blockHash
	for {
		log.Tracef("Getting status for block %s", currentHash)
		currentBlockStatus, err := csm.blockStatusStore.Get(csm.databaseContext, stagingArea, currentHash)
		if database.IsNotFoundError(err) {
			log.Infof("getUnverifiedChainBlocks failed to retrieve with %s\n", currentHash)
			return nil, err
		}
		if err != nil {
			return nil, err
		}
		if currentBlockStatus != externalapi.StatusUTXOPendingVerification {
			log.Tracef("Block %s has status %s. Returning all the "+
				"unverified blocks prior to it: %s", currentHash, currentBlockStatus, unverifiedBlocks)
			return unverifiedBlocks, nil
		}

		log.Tracef("Block %s is unverified. Adding it to the unverified block collection", currentHash)
		unverifiedBlocks = append(unverifiedBlocks, currentHash)

		currentBlockGHOSTDAGData, err := csm.ghostdagDataStore.Get(csm.databaseContext, stagingArea, currentHash, false)
		if database.IsNotFoundError(err) {
			log.Infof("getUnverifiedChainBlocks failed to retrieve with %s\n", currentHash)
			return nil, err
		}
		if err != nil {
			return nil, err
		}

		if currentBlockGHOSTDAGData.SelectedParent() == nil {
			log.Tracef("Genesis block reached. Returning all the "+
				"unverified blocks prior to it: %s", unverifiedBlocks)
			return unverifiedBlocks, nil
		}

		currentHash = currentBlockGHOSTDAGData.SelectedParent()
	}
}

func (csm *consensusStateManager) resolveSingleBlockStatus(stagingArea *model.StagingArea,
	blockHash, selectedParentHash *externalapi.DomainHash, selectedParentPastUTXOSet externalapi.UTXODiff, isResolveTip bool) (
	externalapi.BlockStatus, externalapi.UTXODiff, error,
) {
	onEnd := logger.LogAndMeasureExecutionTime(log, fmt.Sprintf("resolveSingleBlockStatus for %s", blockHash))
	defer onEnd()
	if !csm.genesisHash.Equal(blockHash) && selectedParentPastUTXOSet == nil {
		return 0, nil, errors.Errorf("missing selected parent past UTXO for block %s (selected parent %s)", blockHash, selectedParentHash)
	}

	log.Tracef("Calculating pastUTXO and acceptance data and multiset for block %s", blockHash)
	blockGHOSTDAGData, err := csm.ghostdagDataStore.Get(csm.databaseContext, stagingArea, blockHash, false)
	if database.IsNotFoundError(err) {
		log.Infof("resolveSingleBlockStatus failed to retrieve with %s\n", blockHash)
		return 0, nil, err
	}
	if err != nil {
		return 0, nil, err
	}
	pastUTXOSet, acceptanceData, multiset, err := csm.calculatePastUTXOAndAcceptanceDataWithSelectedParentUTXO(
		stagingArea, blockHash, selectedParentPastUTXOSet, blockGHOSTDAGData)
	if err != nil {
		return 0, nil, err
	}
	if pastUTXOSet == nil {
		return 0, nil, errors.Errorf("calculated past UTXO is nil for block %s", blockHash)
	}

	if csm.genesisHash.Equal(blockHash) {
		log.Tracef("Staging the utxoDiff of genesis")
		csm.stageDiff(stagingArea, blockHash, pastUTXOSet, nil)
		return externalapi.StatusUTXOValid, nil, nil
	}

	log.Tracef("Staging the calculated acceptance data of block %s", blockHash)
	csm.acceptanceDataStore.Stage(stagingArea, blockHash, acceptanceData)

	block, err := csm.blockStore.Block(csm.databaseContext, stagingArea, blockHash)
	if err != nil {
		return 0, nil, err
	}

	log.Tracef("verifying the UTXO of block %s", blockHash)
	err = csm.verifyUTXO(stagingArea, block, blockHash, pastUTXOSet, acceptanceData, multiset)
	if err != nil {
		if errors.As(err, &ruleerrors.RuleError{}) {
			log.Debugf("UTXO verification for block %s failed: %s", blockHash, err)
			log.Tracef("Staging the multiset of disqualified block %s", blockHash)
			csm.multisetStore.Stage(stagingArea, blockHash, multiset)

			utxoDiff, diffErr := selectedParentPastUTXOSet.DiffFrom(pastUTXOSet)
			if diffErr != nil {
				return 0, nil, diffErr
			}
			csm.stageDiff(stagingArea, blockHash, utxoDiff, selectedParentHash)
			// Even for disqualified blocks, return the calculated past UTXO so the
			// next block in the chain can use it when resolving a chain of
			// disqualified statuses.
			return externalapi.StatusDisqualifiedFromChain, pastUTXOSet, nil
		}
		return 0, nil, err
	}
	log.Debugf("UTXO verification for block %s passed", blockHash)

	log.Tracef("Staging the multiset of block %s", blockHash)
	csm.multisetStore.Stage(stagingArea, blockHash, multiset)

	oldSelectedTip, err := csm.virtualSelectedParent(stagingArea)
	if err != nil {
		return 0, nil, err
	}

	if isResolveTip {
		// Check if oldSelectedTip has a UTXO-valid status before trying to restore past UTXO
		// During IBD with headers proof, oldSelectedTip might be a header-only block from the pruning point proof
		oldSelectedTipStatus, err := csm.blockStatusStore.Get(csm.databaseContext, stagingArea, oldSelectedTip)
		if err != nil && !database.IsNotFoundError(err) {
			return 0, nil, err
		}

		var oldSelectedTipUTXOSet externalapi.UTXODiff
		if database.IsNotFoundError(err) || oldSelectedTipStatus != externalapi.StatusUTXOValid {
			// If oldSelectedTip is not UTXO-valid, we can't restore its past UTXO
			// This can happen during IBD with headers proof where the virtual's selected parent
			// is a header-only block from the pruning point proof
			oldSelectedTipUTXOSet = nil
		} else {
			oldSelectedTipUTXOSet, err = csm.restorePastUTXO(stagingArea, oldSelectedTip)
			if err != nil {
				return 0, nil, err
			}
		}
		isNewSelectedTip, err := csm.isNewSelectedTip(stagingArea, blockHash, oldSelectedTip)
		if err != nil {
			return 0, nil, err
		}

		if isNewSelectedTip {
			log.Debugf("Block %s is the new selected tip", blockHash)

			// If oldSelectedTipUTXOSet is nil (old selected tip is header-only), we can't calculate
			// the diff. This can happen during IBD with headers proof.
			if oldSelectedTipUTXOSet != nil {
				updatedOldSelectedTipUTXOSet, err := pastUTXOSet.DiffFrom(oldSelectedTipUTXOSet)
				if err != nil {
					return 0, nil, err
				}
				log.Debugf("Setting the old selected tip's (%s) diffChild to be the new selected tip (%s)",
					oldSelectedTip, blockHash)
				csm.stageDiff(stagingArea, oldSelectedTip, updatedOldSelectedTipUTXOSet, blockHash)
			} else {
				log.Debugf("Old selected tip %s is header-only, skipping UTXO diff child update", oldSelectedTip)
			}

			log.Tracef("Staging the utxoDiff of block %s, with virtual as diffChild", blockHash)
			csm.stageDiff(stagingArea, blockHash, pastUTXOSet, nil)
		} else {
			log.Debugf("Block %s is the tip of currently resolved chain, but not the new selected tip,"+
				"therefore setting it's utxoDiffChild to be the current selectedTip %s", blockHash, oldSelectedTip)
			// If oldSelectedTipUTXOSet is nil, we can't calculate the diff
			if oldSelectedTipUTXOSet != nil {
				utxoDiff, err := oldSelectedTipUTXOSet.DiffFrom(pastUTXOSet)
				if err != nil {
					return 0, nil, err
				}
				csm.stageDiff(stagingArea, blockHash, utxoDiff, oldSelectedTip)
			} else {
				// oldSelectedTip is header-only, so we set the diffChild to the selected parent instead
				utxoDiff, err := selectedParentPastUTXOSet.DiffFrom(pastUTXOSet)
				if err != nil {
					return 0, nil, err
				}
				csm.stageDiff(stagingArea, blockHash, utxoDiff, selectedParentHash)
			}
		}
	} else {
		// If the block is not the tip of the currently resolved chain, we set it's diffChild to be the selectedParent,
		// this is a temporary measure to ensure there's a restore path to all blocks at all times.
		// Later down the process, the diff will be reversed in reverseUTXODiffs.
		log.Debugf("Block %s is not the new selected tip, and is not the tip of the currently verified chain, "+
			"therefore temporarily setting selectedParent as it's diffChild", blockHash)
		utxoDiff, err := selectedParentPastUTXOSet.DiffFrom(pastUTXOSet)
		if err != nil {
			return 0, nil, err
		}

		csm.stageDiff(stagingArea, blockHash, utxoDiff, selectedParentHash)
	}

	return externalapi.StatusUTXOValid, pastUTXOSet, nil
}

func (csm *consensusStateManager) isNewSelectedTip(stagingArea *model.StagingArea,
	blockHash, oldSelectedTip *externalapi.DomainHash,
) (bool, error) {
	newSelectedTip, err := csm.ghostdagManager.ChooseSelectedParent(stagingArea, blockHash, oldSelectedTip)
	if database.IsNotFoundError(err) {
		log.Infof("isNewSelectedTip failed to retrieve with %s\n", oldSelectedTip)
		return false, err
	}
	if err != nil {
		return false, err
	}

	return blockHash.Equal(newSelectedTip), nil
}

func (csm *consensusStateManager) virtualSelectedParent(stagingArea *model.StagingArea) (*externalapi.DomainHash, error) {
	virtualGHOSTDAGData, err := csm.ghostdagDataStore.Get(csm.databaseContext, stagingArea, model.VirtualBlockHash, false)
	if err != nil {
		return nil, err
	}

	return virtualGHOSTDAGData.SelectedParent(), nil
}
