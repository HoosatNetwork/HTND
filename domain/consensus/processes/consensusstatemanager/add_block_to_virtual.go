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
		}
	}
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

func (csm *consensusStateManager) FindHighestValidBlock() (*externalapi.DomainHash, error) {
	// Get an iterator to go through all blocks
	iterator, err := csm.blockStore.AllBlockHashesIterator(csm.databaseContext)
	if err != nil {
		return nil, err
	}
	defer iterator.Close()
	log.Infof("Found zero tips, so searching for something we could use. This may take a moment.")
	// Iterate through all blocks

	var highestBlockHash *externalapi.DomainHash
	var highestDAaScore uint64 = 0
	if iterator.First() {
		for {
			blockHash, err := iterator.Get()
			if err != nil {
				return nil, err
			}
			if blockHash.Equal(model.VirtualBlockHash) || blockHash.Equal(model.VirtualGenesisBlockHash) {
				continue
			}

			// Use a separate staging area for each block to avoid memory accumulation
			stagingArea := model.NewStagingArea()

			status, err := csm.blockStatusStore.Get(csm.databaseContext, stagingArea, blockHash)
			if err != nil {
				// Skip blocks that can't be retrieved (might be pruned)
				log.Infof("Error happened on getting block status")
				if !iterator.Next() {
					break
				}
				continue
			}
			if status == externalapi.StatusUTXOValid {
				block, err := csm.blockStore.Block(csm.databaseContext, stagingArea, blockHash)
				if err != nil {
					continue
				}
				log.Debugf("Found valid block %s, DAAScore %d", blockHash, block.Header.DAAScore())
				if block.Header.DAAScore() > highestDAaScore {
					highestBlockHash = blockHash
					highestDAaScore = block.Header.DAAScore()
				}
			}

			if !iterator.Next() {
				break
			}
		}
	}
	if highestBlockHash == nil {
		return nil, errors.Errorf("FindHighestValidBlock: no StatusUTXOValid block in store")
	}
	log.Infof("Highest valid block %s at DAAScore %d found for tip", highestBlockHash, highestDAaScore)
	return highestBlockHash, nil
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
	// Disqualified blocks are not kept as tips, so a template never merges one, but each is replaced below by its
	// ancestors that only it covered.
	var disqualifiedTips []*externalapi.DomainHash

	// Check the new block's status before adding to tips
	if newTipStatus != externalapi.StatusDisqualifiedFromChain {
		if newTipStatus == externalapi.StatusUTXOValid && !newTipHash.Equal(model.VirtualBlockHash) && !newTipHash.Equal(model.VirtualGenesisBlockHash) {
			csm.lastValidBlock = newTipHash
		}
		newTips = append(newTips, newTipHash)
	} else {
		disqualifiedTips = append(disqualifiedTips, newTipHash)
	}

	for _, currentTip := range currentTips {
		if newTipParentsSet.Contains(currentTip) {
			continue
		}

		status, err := csm.blockStatusStore.Get(csm.databaseContext, stagingArea, currentTip)
		if database.IsNotFoundError(err) {
			continue
		}
		if err != nil {
			return nil, err
		}

		if status == externalapi.StatusDisqualifiedFromChain {
			disqualifiedTips = append(disqualifiedTips, currentTip)
			continue
		}
		newTips = append(newTips, currentTip)
	}
	newTips, err = csm.appendAncestorsCoveredOnlyByDisqualified(stagingArea, newTips, disqualifiedTips)
	if err != nil {
		return nil, err
	}
	newTips, err = csm.dropTipsInPastOfOtherTips(stagingArea, newTips)
	if err != nil {
		return nil, err
	}
	if len(newTips) < 1 {
		if csm.lastValidBlock != nil {
			newTips = append(newTips, csm.lastValidBlock)
		}
	}

	if len(newTips) < 1 {
		candidate, err := csm.FindHighestValidBlock()
		if err != nil {
			return nil, err
		}
		if candidate == nil {
			return nil, errors.Errorf("calculateNewTips: no tips left and no UTXO-valid block found")
		}
		newTips = append(newTips, candidate)
	}
	log.Debugf("The new number of tips is: %d", len(newTips))

	return newTips, nil
}

// appendAncestorsCoveredOnlyByDisqualified appends to tips the blocks that disqualifiedTips leave uncovered: walking back
// from them through disqualified blocks, every block reached that is not disqualified and has no child that is UTXO-valid
// or pending verification. These are the blocks kaspad's selectVirtualSelectedParent falls back to from a disqualified
// tip (it pushes a parent once all its children are disqualified). Dropping a disqualified tip without them took its
// valid ancestors out of the running for virtual's selected parent: a single invalid child of virtual's selected parent
// moved virtual onto a lighter branch, or left no tip at all.
//
// Only which tips virtual's parents are picked from changes. A block covered by an eligible child is never appended, and
// no disqualified block becomes a tip. An appended block can still be in another tip's past, through a disqualified
// child that tip merges; dropTipsInPastOfOtherTips removes it.
func (csm *consensusStateManager) appendAncestorsCoveredOnlyByDisqualified(stagingArea *model.StagingArea,
	tips, disqualifiedTips []*externalapi.DomainHash,
) ([]*externalapi.DomainHash, error) {
	if len(disqualifiedTips) == 0 {
		return tips, nil
	}
	visited := hashset.New()
	for _, tip := range tips {
		visited.Add(tip)
	}
	for _, tip := range disqualifiedTips {
		visited.Add(tip)
	}
	queue := disqualifiedTips
	for len(queue) > 0 {
		var block *externalapi.DomainHash
		block, queue = queue[0], queue[1:]
		parents, err := csm.dagTopologyManager.Parents(stagingArea, block)
		if err != nil {
			return nil, err
		}
		for _, parent := range parents {
			if visited.Contains(parent) || parent.Equal(model.VirtualGenesisBlockHash) {
				continue
			}
			visited.Add(parent)

			status, err := csm.blockStatusStore.Get(csm.databaseContext, stagingArea, parent)
			if database.IsNotFoundError(err) {
				continue
			}
			if err != nil {
				return nil, err
			}
			if status != externalapi.StatusDisqualifiedFromChain && status != externalapi.StatusUTXOValid &&
				status != externalapi.StatusUTXOPendingVerification {
				continue
			}
			hasEligibleChild, err := csm.hasValidOrPendingChild(stagingArea, parent)
			if err != nil {
				return nil, err
			}
			if hasEligibleChild {
				continue // that child, or a tip in its future, covers the parent and its past
			}
			if status == externalapi.StatusDisqualifiedFromChain {
				queue = append(queue, parent)
				continue
			}
			tips = append(tips, parent)
		}
	}
	return tips, nil
}

// hasValidOrPendingChild is the walk's "covered" rule. A child with StatusInvalid does not cover its parent here, while
// selectVirtualSelectedParent's getRelevantChildCount counts it as a child that is never disqualified, so it never falls
// back to that parent. An invalid block can carry no chain, so its parent is only covered by its other children; the
// walk is the more permissive of the two on purpose, and a block it appends needs no fall-back.
func (csm *consensusStateManager) hasValidOrPendingChild(stagingArea *model.StagingArea,
	block *externalapi.DomainHash,
) (bool, error) {
	children, err := csm.dagTopologyManager.Children(stagingArea, block)
	if err != nil {
		return false, err
	}
	for _, child := range children {
		if child.Equal(model.VirtualBlockHash) {
			continue
		}
		status, err := csm.blockStatusStore.Get(csm.databaseContext, stagingArea, child)
		if database.IsNotFoundError(err) {
			continue
		}
		if err != nil {
			return false, err
		}
		if status == externalapi.StatusUTXOValid || status == externalapi.StatusUTXOPendingVerification {
			return true, nil
		}
	}
	return false, nil
}

// dropTipsInPastOfOtherTips keeps the tips an antichain, as kaspad's are. Only a tip with a child can be in another tip's
// past: a block appended by appendAncestorsCoveredOnlyByDisqualified whose disqualified child another tip merges (in
// either order), or a block whose children have no body. pickVirtualParents skips a candidate only in the past of the
// parents it has already chosen, and with at least maxBlockParents candidates it moves the lightest ones ahead, so such
// a tip could become a virtual parent next to a block in its future, which checkParentsIncest rejects from
// ParentsIncestVersion on.
func (csm *consensusStateManager) dropTipsInPastOfOtherTips(stagingArea *model.StagingArea,
	tips []*externalapi.DomainHash,
) ([]*externalapi.DomainHash, error) {
	if len(tips) < 2 {
		return tips, nil
	}
	kept := make([]*externalapi.DomainHash, 0, len(tips))
	for _, tip := range tips {
		children, err := csm.dagTopologyManager.Children(stagingArea, tip)
		if err != nil {
			return nil, err
		}
		hasChild := false
		for _, child := range children {
			if !child.Equal(model.VirtualBlockHash) {
				hasChild = true
				break
			}
		}
		if hasChild {
			others := make([]*externalapi.DomainHash, 0, len(tips)-1)
			for _, other := range tips {
				if !other.Equal(tip) {
					others = append(others, other)
				}
			}
			inPastOfOtherTip, err := csm.dagTopologyManager.IsAncestorOfAny(stagingArea, tip, others)
			if err != nil {
				return nil, err
			}
			if inPastOfOtherTip {
				continue
			}
		}
		kept = append(kept, tip)
	}
	return kept, nil
}
