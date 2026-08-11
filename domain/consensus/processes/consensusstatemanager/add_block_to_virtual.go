package consensusstatemanager

import (
	"github.com/HoosatNetwork/HTND/domain/consensus/database"
	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/hashset"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/utxo"
	"github.com/HoosatNetwork/HTND/infrastructure/logger"
)

// AddBlock submits the given block to be added to the
// current virtual. This process may result in a new virtual block
// getting created
func (csm *consensusStateManager) AddBlock(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash, updateVirtual bool) (
	*externalapi.SelectedChainPath, externalapi.UTXODiff, *model.UTXODiffReversalData, error,
) {
	onEnd := logger.LogAndMeasureExecutionTime(log, "csm.AddBlock")
	defer onEnd()

	var blockStatus externalapi.BlockStatus
	var reversalData *model.UTXODiffReversalData
	if updateVirtual {
		log.Debugf("Resolving whether the block %s is the next virtual selected parent", blockHash)
		isCandidateToBeNextVirtualSelectedParent, err := csm.isCandidateToBeNextVirtualSelectedParent(stagingArea, blockHash)
		if err != nil {
			return nil, nil, nil, err
		}

		if isCandidateToBeNextVirtualSelectedParent {
			// It's important to check for finality violation before resolving the block status, because the status of
			// blocks with a selected chain that doesn't contain the pruning point cannot be resolved because they will
			// eventually try to fetch UTXO diffs from the past of the pruning point.
			log.Debugf("Block %s is candidate to be the next virtual selected parent. Resolving whether it violates "+
				"finality", blockHash)
			isViolatingFinality, shouldNotify, err := csm.isViolatingFinality(stagingArea, blockHash)
			if err != nil {
				return nil, nil, nil, err
			}

			if shouldNotify {
				// TODO: Send finality conflict notification
				log.Warnf("Finality Violation Detected! Block %s violates finality!", blockHash)
			}

			if !isViolatingFinality {
				log.Debugf("Block %s doesn't violate finality. Resolving its block status", blockHash)
				// Keep the block-resolution path in a single staging area so block insertion
				// does not create unnecessary per-block commits while still preserving the
				// same UTXO-status and diff-child semantics.
				blockStatus, reversalData, err = csm.ResolveBlockStatus(stagingArea, blockHash, false)
				if err != nil {
					return nil, nil, nil, err
				}

				log.Debugf("Block %s resolved to status `%s`", blockHash, blockStatus)
			}
		} else {
			log.Debugf("Block %s is not the next virtual selected parent, "+
				"therefore its status remains `%s`", blockHash, externalapi.StatusUTXOPendingVerification)
			// Keep the block-resolution path in a single staging area so block insertion
			// does not create unnecessary per-block commits while still preserving the
			// same UTXO-status and diff-child semantics.
			blockStatus, reversalData, err = csm.ResolveBlockStatus(stagingArea, blockHash, false)
			if err != nil {
				return nil, nil, nil, err
			}
			log.Debugf("Block %s resolved to status `%s`", blockHash, blockStatus)
		}
	}
	// Just commented out code, for future testing.
	// if blockStatus == externalapi.StatusInvalid || blockStatus == externalapi.StatusDisqualifiedFromChain {
	// 	return nil, nil, nil, errors.Wrapf(ruleerrors.ErrDuplicateBlock, "block %s is disqualified or invalid", blockHash)
	// }
	log.Debugf("Adding block %s to the DAG tips", blockHash)
	newTips, err := csm.addTip(stagingArea, blockHash, blockStatus)
	if err != nil {
		return nil, nil, nil, err
	}
	log.Debugf("After adding %s, the amount of new tips are %d", blockHash, len(newTips))

	if !updateVirtual {
		return &externalapi.SelectedChainPath{}, utxo.NewUTXODiff(), nil, nil
	}

	log.Debugf("Updating the virtual with the new tips")
	selectedParentChainChanges, virtualUTXODiff, err := csm.updateVirtual(stagingArea, blockHash, newTips)
	if err != nil {
		return nil, nil, nil, err
	}

	return selectedParentChainChanges, virtualUTXODiff, reversalData, nil
}

func (csm *consensusStateManager) isCandidateToBeNextVirtualSelectedParent(
	stagingArea *model.StagingArea, blockHash *externalapi.DomainHash,
) (bool, error) {
	log.Tracef("isCandidateToBeNextVirtualSelectedParent start for block %s", blockHash)
	defer log.Tracef("isCandidateToBeNextVirtualSelectedParent end for block %s", blockHash)

	if blockHash.Equal(csm.genesisHash) {
		log.Debugf("Block %s is the genesis block, therefore it is "+
			"the selected parent by definition", blockHash)
		return true, nil
	}

	virtualGhostdagData, err := csm.ghostdagDataStore.Get(csm.databaseContext, stagingArea, model.VirtualBlockHash, false)
	if database.IsNotFoundError(err) {
		log.Infof("isCandidateToBeNextVirtualSelectedParent failed to retrieve with %s\n", model.VirtualBlockHash)
		return false, err
	}
	if err != nil {
		return false, err
	}

	log.Debugf("Selecting the next selected parent between "+
		"the block %s the current selected parent %s", blockHash, virtualGhostdagData.SelectedParent())
	nextVirtualSelectedParent, err := csm.ghostdagManager.ChooseSelectedParent(
		stagingArea, virtualGhostdagData.SelectedParent(), blockHash)
	if err != nil {
		return false, err
	}
	log.Debugf("The next selected parent is: %s", nextVirtualSelectedParent)

	return blockHash.Equal(nextVirtualSelectedParent), nil
}

func (csm *consensusStateManager) addTip(stagingArea *model.StagingArea, newTipHash *externalapi.DomainHash, newTipStatus externalapi.BlockStatus) (newTips []*externalapi.DomainHash, err error) {
	log.Tracef("addTip start for new tip %s", newTipHash)
	defer log.Tracef("addTip end for new tip %s", newTipHash)

	log.Debugf("Calculating the new tips for new tip %s", newTipHash)
	newTips, err = csm.calculateNewTips(stagingArea, newTipHash, newTipStatus)
	if err != nil {
		return nil, err
	}

	csm.consensusStateStore.StageTips(stagingArea, newTips)
	log.Debugf("Staged the new tips, len: %d", len(newTips))

	return newTips, nil
}

func (csm *consensusStateManager) calculateNewTips(
	stagingArea *model.StagingArea, newTipHash *externalapi.DomainHash, newTipStatus externalapi.BlockStatus,
) ([]*externalapi.DomainHash, error) {
	log.Tracef("calculateNewTips start for new tip %s", newTipHash)
	defer log.Tracef("calculateNewTips end for new tip %s", newTipHash)

	if newTipHash.Equal(csm.genesisHash) {
		log.Debugf("The new tip is the genesis block, therefore it is the only tip by definition")
		return []*externalapi.DomainHash{newTipHash}, nil
	}

	currentTips, err := csm.consensusStateStore.Tips(stagingArea, csm.databaseContext)
	if err != nil {
		return nil, err
	}

	newTipParents, err := csm.dagTopologyManager.Parents(stagingArea, newTipHash)
	if err != nil {
		return nil, err
	}
	log.Debugf("The parents of the new tip are: %s", newTipParents)

	newTipParentsSet := hashset.New()
	for _, parent := range newTipParents {
		newTipParentsSet.Add(parent)
	}

	newTips := make([]*externalapi.DomainHash, 0, 1+len(currentTips))

	// Check the new block's status before adding to tips
	if newTipStatus == externalapi.StatusDisqualifiedFromChain || newTipStatus == externalapi.StatusInvalid {
		log.Debugf("Dropping disqualified/invalid new tip %s", newTipHash)
	} else {
		newTips = append(newTips, newTipHash)
	}

	for _, currentTip := range currentTips {
		if newTipParentsSet.Contains(currentTip) {
			continue
		}

		status, err := csm.blockStatusStore.Get(csm.databaseContext, stagingArea, currentTip)
		if err != nil {
			continue
		}

		if status == externalapi.StatusDisqualifiedFromChain || status == externalapi.StatusInvalid {
			// Just drop it. Do NOT walk its parents.
			log.Infof("Dropping disqualified/invalid tip %s", currentTip)
			continue
		}
		newTips = append(newTips, currentTip)
	}
	log.Debugf("The new number of tips is: %d", len(newTips))

	return newTips, nil
}
