package ghostdagmanager

import (
	"sort"

	"github.com/HoosatNetwork/HTND/domain/consensus/database"
	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/constants"

	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
)

func (gm *ghostdagManager) mergeSetWithoutSelectedParent(stagingArea *model.StagingArea,
	selectedParent *externalapi.DomainHash, blockParents []*externalapi.DomainHash, k externalapi.KType,
) ([]*externalapi.DomainHash, error) {
	mergeSetMap := make(map[externalapi.DomainHash]struct{}, k)
	mergeSetSlice := make([]*externalapi.DomainHash, 0, k)
	selectedParentPast := make(map[externalapi.DomainHash]struct{})
	queue := []*externalapi.DomainHash{}
	// Queueing all parents (other than the selected parent itself) for processing.
	for _, parent := range blockParents {
		if parent.Equal(selectedParent) {
			continue
		}
		mergeSetMap[*parent] = struct{}{}
		mergeSetSlice = append(mergeSetSlice, parent)
		queue = append(queue, parent)
	}

	for len(queue) > 0 {
		var current *externalapi.DomainHash
		current, queue = queue[0], queue[1:]
		// For each parent of the current block we check whether it is in the past of the selected parent. If not,
		// we add the it to the resulting anticone-set and queue it for further processing.
		currentParents, err := gm.dagTopologyManager.Parents(stagingArea, current)
		if err != nil {
			return nil, err
		}
		for _, parent := range currentParents {
			if _, ok := mergeSetMap[*parent]; ok {
				continue
			}

			if _, ok := selectedParentPast[*parent]; ok {
				continue
			}

			isAncestorOfSelectedParent, err := gm.dagTopologyManager.IsAncestorOf(stagingArea, parent, selectedParent)
			if err != nil {
				return nil, err
			}

			if isAncestorOfSelectedParent {
				selectedParentPast[*parent] = struct{}{}
				continue
			}

			mergeSetMap[*parent] = struct{}{}
			mergeSetSlice = append(mergeSetSlice, parent)
			queue = append(queue, parent)
		}
	}

	err := gm.sortMergeSet(stagingArea, mergeSetSlice)
	if err != nil {
		return nil, err
	}

	return mergeSetSlice, nil
}

func (gm *ghostdagManager) sortMergeSet(stagingArea *model.StagingArea, mergeSetSlice []*externalapi.DomainHash) error {
	var err error
	sort.Slice(mergeSetSlice, func(i, j int) bool {
		if err != nil {
			return false
		}
		isLess, lessErr := gm.less(stagingArea, mergeSetSlice[i], mergeSetSlice[j])
		if lessErr != nil {
			err = lessErr
			return false
		}
		return isLess
	})
	return err
}

// GetSortedMergeSet return the merge set sorted in a toplogical order.
func (gm *ghostdagManager) GetSortedMergeSet(stagingArea *model.StagingArea,
	current *externalapi.DomainHash,
) ([]*externalapi.DomainHash, error) {
	currentGhostdagData, err := gm.ghostdagDataStore.Get(gm.databaseContext, stagingArea, current, false)
	if database.IsNotFoundError(err) {
		log.Infof("GetSortedMergeSet failed to retrieve with %s\n", current)
		return nil, err
	}
	if err != nil {
		return nil, err
	}

	blueMergeSet := currentGhostdagData.MergeSetBlues()
	redMergeSet := currentGhostdagData.MergeSetReds()
	sortedMergeSet := make([]*externalapi.DomainHash, 0, len(blueMergeSet)+len(redMergeSet))
	// If the current block is the genesis block:
	if len(blueMergeSet) == 0 {
		return sortedMergeSet, nil
	}

	// Determine which block to place first. For blocks at or above
	// SelectedParentFixActivationDAAScore we use the authoritative GHOSTDAG
	// SelectedParent() so that the first element is always the true selected
	// parent regardless of the order in MergeSetBlues (which DAGKnight reordering
	// may have shuffled). For earlier blocks we fall back to MergeSetBlues()[0] —
	// the legacy behaviour — so that re-validation agrees with the chain that all
	// nodes committed to while running the pre-fix code.
	//
	// DAAScore is looked up from the block header. For the virtual block (which has
	// no stored header) we default to the new behaviour because the virtual block is
	// always current and therefore above the activation score.
	useFixedOrdering := true // default for virtual / header-not-found cases
	if !current.Equal(model.VirtualBlockHash) {
		header, headerErr := gm.headerStore.BlockHeader(gm.databaseContext, stagingArea, current)
		if headerErr == nil {
			useFixedOrdering = header.DAAScore() >= constants.SelectedParentFixActivationDAAScore
		}
	}

	var selectedParent *externalapi.DomainHash
	if useFixedOrdering {
		// Post-activation: selected parent is authoritative from GHOSTDAG metadata.
		selectedParent = currentGhostdagData.SelectedParent()
	} else {
		// Pre-activation: preserve legacy ordering where MergeSetBlues()[0] was
		// treated as the selected parent by applyMergeSetBlocks.
		selectedParent = blueMergeSet[0]
	}
	sortedMergeSet = append(sortedMergeSet, selectedParent)

	// Build the remaining blue slice, excluding the selected parent by hash so the
	// function is correct even when the selected parent is not at index 0.
	remainingBlues := make([]*externalapi.DomainHash, 0, len(blueMergeSet))
	for _, b := range blueMergeSet {
		if !b.Equal(selectedParent) {
			remainingBlues = append(remainingBlues, b)
		}
	}
	blueMergeSet = remainingBlues
	i, j := 0, 0
	for i < len(blueMergeSet) && j < len(redMergeSet) {
		currentBlue := blueMergeSet[i]
		currentBlueGhostdagData, err := gm.ghostdagDataStore.Get(gm.databaseContext, stagingArea, currentBlue, false)
		if err != nil {
			return nil, err
		}
		currentRed := redMergeSet[j]
		currentRedGhostdagData, err := gm.ghostdagDataStore.Get(gm.databaseContext, stagingArea, currentRed, false)
		if err != nil {
			return nil, err
		}
		if gm.Less(currentBlue, currentBlueGhostdagData, currentRed, currentRedGhostdagData) {
			sortedMergeSet = append(sortedMergeSet, currentBlue)
			i++
		} else {
			sortedMergeSet = append(sortedMergeSet, currentRed)
			j++
		}
	}
	sortedMergeSet = append(sortedMergeSet, blueMergeSet[i:]...)
	sortedMergeSet = append(sortedMergeSet, redMergeSet[j:]...)

	return sortedMergeSet, nil
}
