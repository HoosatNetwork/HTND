package syncmanager

import (
	"github.com/HoosatNetwork/HTND/domain/consensus/database"
	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/hashset"
	"github.com/pkg/errors"
)

// antiPastHashesBetween returns the hashes of the blocks between the
// lowHash's antiPast and highHash's antiPast, or up to `maxBlocks`, if non-zero.
// The result excludes lowHash and includes highHash. If lowHash == highHash, returns nothing.
// If maxBlocks != 0 then maxBlocks MUST be >= MergeSetSizeLimit + 1
// because it returns blocks with MergeSet granularity,
// so if MergeSet > maxBlocks, function will return nothing
func (sm *syncManager) antiPastHashesBetween(stagingArea *model.StagingArea, lowHash, highHash *externalapi.DomainHash,
	maxBlocks uint64,
) (hashes []*externalapi.DomainHash, actualHighHash *externalapi.DomainHash, err error) {
	// Sanity check, for debugging only
	if maxBlocks != 0 && maxBlocks < sm.mergeSetSizeLimit+1 {
		return nil, nil,
			errors.Errorf("maxBlocks (%d) MUST be >= MergeSetSizeLimit + 1 (%d)", maxBlocks, sm.mergeSetSizeLimit+1)
	}

	// If lowHash is not in the selectedParentChain of highHash - SelectedChildIterator will fail.
	// Therefore, we traverse down lowHash's selectedParentChain until we reach a block that is in
	// highHash's selectedParentChain.
	// We keep originalLowHash to filter out blocks in it's past later down the road
	originalLowHash := lowHash
	lowHash, err = sm.findLowHashInHighHashSelectedParentChain(stagingArea, lowHash, highHash)
	if err != nil {
		return nil, nil, err
	}

	lowBlockGHOSTDAGData, err := sm.ghostdagDataStore.Get(sm.databaseContext, stagingArea, lowHash, false)
	if database.IsNotFoundError(err) {
		log.Infof("antiPastHashesBetween failed to retrieve low with %s\n", lowHash)
		return nil, nil, err
	}
	if err != nil {
		return nil, nil, err
	}
	highBlockGHOSTDAGData, err := sm.ghostdagDataStore.Get(sm.databaseContext, stagingArea, highHash, false)
	if database.IsNotFoundError(err) {
		log.Infof("antiPastHashesBetween failed to retrieve high with %s\n", highHash)
		return nil, nil, err
	}
	if err != nil {
		return nil, nil, err
	}
	if lowBlockGHOSTDAGData.BlueScore() > highBlockGHOSTDAGData.BlueScore() {
		return nil, nil, errors.Errorf("low hash blueScore > high hash blueScore (%d > %d)",
			lowBlockGHOSTDAGData.BlueScore(), highBlockGHOSTDAGData.BlueScore())
	}

	// Collect all hashes by concatenating the merge-sets of all blocks between highHash and lowHash
	blockHashes := []*externalapi.DomainHash{}
	iterator, err := sm.dagTraversalManager.SelectedChildIterator(stagingArea, highHash, lowHash, false)
	if err != nil {
		return nil, nil, err
	}
	defer iterator.Close()
	for ok := iterator.First(); ok; ok = iterator.Next() {
		current, err := iterator.Get()
		if err != nil {
			return nil, nil, err
		}
		// Both blue and red merge sets are topologically sorted, but not the concatenation of the two.
		// We require the blocks to be topologically sorted. In addition,  for optimal performance,
		// we want the selectedParent to be first.
		// Since the rest of the merge set is in the anticone of selectedParent, it's position in the list does not
		// matter, even though it's blue score is the highest, we can arbitrarily decide it comes first.
		// Therefore we first append the selectedParent, then the rest of blocks in ghostdag order.
		sortedMergeSet, err := sm.ghostdagManager.GetSortedMergeSet(stagingArea, current)
		if err != nil {
			return nil, nil, err
		}

		total := len(blockHashes) + len(sortedMergeSet)
		if total < 0 {
			// Should never happen, but guard for safety
			break
		}
		if maxBlocks != 0 && uint64(total) > maxBlocks {
			break
		}

		highHash = current

		// append to blockHashes all blocks in sortedMergeSet which are not in the past of originalLowHash
		for _, blockHash := range sortedMergeSet {
			isInPastOfOriginalLowHash, err := sm.dagTopologyManager.IsAncestorOf(stagingArea, blockHash, originalLowHash)
			if err != nil {
				return nil, nil, err
			}
			if isInPastOfOriginalLowHash {
				continue
			}
			blockHashes = append(blockHashes, blockHash)
		}
	}

	// The process above doesn't return highHash, so include it explicitly, unless highHash == lowHash
	if !lowHash.Equal(highHash) {
		blockHashes = append(blockHashes, highHash)
	}

	return blockHashes, highHash, nil
}

// antiPastHashesBetween returns the hashes of the blocks between the
// lowHash's antiPast and highHash's antiPast, or up to `maxBlocks`, if non-zero.
// The result excludes lowHash and includes highHash. If lowHash == highHash, returns nothing.
// If maxBlocks != 0 then maxBlocks MUST be >= MergeSetSizeLimit + 1
// because it returns blocks with MergeSet granularity,
// so if MergeSet > maxBlocks, function will return nothing
func (sm *syncManager) antiPastHashesBetweenBrute(stagingArea *model.StagingArea, lowHash, highHash *externalapi.DomainHash,
	maxBlocks uint64,
) (hashes []*externalapi.DomainHash, actualHighHash *externalapi.DomainHash, err error) {
	// Sanity check, for debugging only
	if maxBlocks != 0 && maxBlocks < sm.mergeSetSizeLimit+1 {
		return nil, nil,
			errors.Errorf("maxBlocks (%d) MUST be >= MergeSetSizeLimit + 1 (%d)", maxBlocks, sm.mergeSetSizeLimit+1)
	}

	// If lowHash is not in the selectedParentChain of highHash - SelectedChildIterator will fail.
	// Therefore, we traverse down lowHash's selectedParentChain until we reach a block that is in
	// highHash's selectedParentChain.
	// We keep originalLowHash to filter out blocks in it's past later down the road
	originalLowHash := lowHash
	lowHash, err = sm.findLowHashInHighHashSelectedParentChain(stagingArea, lowHash, highHash)
	if err != nil {
		return nil, nil, err
	}
	if !originalLowHash.Equal(lowHash) {
		log.Infof("originalLowHash %s changed to %s", originalLowHash, lowHash)
	}

	lowBlockGHOSTDAGData, err := sm.ghostdagDataStore.Get(sm.databaseContext, stagingArea, lowHash, false)
	if database.IsNotFoundError(err) {
		log.Infof("antiPastHashesBetween failed to retrieve low with %s\n", lowHash)
		return nil, nil, err
	}
	if err != nil {
		return nil, nil, err
	}
	highBlockGHOSTDAGData, err := sm.ghostdagDataStore.Get(sm.databaseContext, stagingArea, highHash, false)
	if database.IsNotFoundError(err) {
		log.Infof("antiPastHashesBetween failed to retrieve high with %s\n", highHash)
		return nil, nil, err
	}
	if err != nil {
		return nil, nil, err
	}
	if lowBlockGHOSTDAGData.BlueScore() > highBlockGHOSTDAGData.BlueScore() {
		return nil, nil, errors.Errorf("low hash blueScore > high hash blueScore (%d > %d)",
			lowBlockGHOSTDAGData.BlueScore(), highBlockGHOSTDAGData.BlueScore())
	}

	log.Infof("Low %s, High %s", lowHash, highHash)
	// Collect all hashes by concatenating the merge-sets of all blocks between highHash and lowHash
	blockHashes := []*externalapi.DomainHash{}
	iterator, err := sm.dagTraversalManager.SelectedChildIterator(stagingArea, highHash, lowHash, false)
	if err != nil {
		return nil, nil, err
	}
	defer iterator.Close()
	seen := make(map[externalapi.DomainHash]struct{})
	for ok := iterator.First(); ok; ok = iterator.Next() {
		current, err := iterator.Get()
		if err != nil {
			return nil, nil, err
		}
		// log.Infof("Current block %s", current)
		header, err := sm.blockHeaderStore.BlockHeader(sm.databaseContext, stagingArea, current)
		if err != nil {
			return nil, nil, err
		}
		// Collect ancestors up to depth 5
		const maxDepth = 5
		levels := make([][]*externalapi.DomainHash, maxDepth)

		// Level 0 = direct parents of current
		for _, blockLevelParent := range header.Parents() {
			for _, parent := range blockLevelParent {
				if _, exists := seen[*parent]; !exists {
					seen[*parent] = struct{}{}
					levels[0] = append(levels[0], parent)
				}
			}
		}

		// Levels 1 and 2 = parents of the previous level
		for depth := 1; depth < maxDepth; depth++ {
			for _, p := range levels[depth-1] {
				h, err := sm.blockHeaderStore.BlockHeader(sm.databaseContext, stagingArea, p)
				if err != nil {
					return nil, nil, err
				}
				for _, blockLevelParent := range h.Parents() {
					for _, parent := range blockLevelParent {
						if _, exists := seen[*parent]; !exists {
							seen[*parent] = struct{}{}
							levels[depth] = append(levels[depth], parent)
						}
					}
				}
			}
		}

		// log.Infof("Printing current block parents")
		// for i, blockhash := range levels[0] {
		// 	log.Infof("%d %s", i, blockhash)
		// }

		total := len(blockHashes) + len(levels[0])
		if total < 0 {
			break
		}
		if maxBlocks != 0 && uint64(total) > maxBlocks {
			break
		}

		highHash = current

		// Append in the original order: deepest first → parents last
		for depth := maxDepth - 1; depth >= 0; depth-- {
			for _, blockHash := range levels[depth] {
				isInPastOfOriginalLowHash, err := sm.dagTopologyManager.IsAncestorOf(stagingArea, blockHash, originalLowHash)
				if err != nil {
					return nil, nil, err
				}
				if isInPastOfOriginalLowHash {
					log.Debugf("Dismissing %s from mergeset, parent of %s, because is in past of original low hash %s",
						blockHash, current, originalLowHash)
					continue
				}
				blockHashes = append(blockHashes, blockHash)
			}
		}
	}

	// The process above doesn't return highHash, so include it explicitly, unless highHash == lowHash
	if !lowHash.Equal(highHash) {
		blockHashes = append(blockHashes, highHash)
	}
	blockHashes = hashset.NewFromSlice(blockHashes...).ToSlice()
	// log.Infof("Printing current block hashes")
	// for i, blockhash := range blockHashes {
	// 	log.Infof("%d %s", i, blockhash)
	// }
	return blockHashes, highHash, nil
}

func (sm *syncManager) findLowHashInHighHashSelectedParentChain(stagingArea *model.StagingArea,
	lowHash *externalapi.DomainHash, highHash *externalapi.DomainHash,
) (*externalapi.DomainHash, error) {
	for {
		isInSelectedParentChain, err := sm.dagTopologyManager.IsInSelectedParentChainOf(stagingArea, lowHash, highHash)
		if err != nil {
			return nil, err
		}
		if isInSelectedParentChain {
			break
		}
		lowBlockGHOSTDAGData, err := sm.ghostdagDataStore.Get(sm.databaseContext, stagingArea, lowHash, false)
		if database.IsNotFoundError(err) {
			log.Infof("findLowHashInHighHashSelectedParentChain failed to retrieve with %s\n", lowHash)
			return nil, err
		}
		if err != nil {
			return nil, err
		}
		lowHash = lowBlockGHOSTDAGData.SelectedParent()
	}
	return lowHash, nil
}

func (sm *syncManager) missingBlockBodyHashes(stagingArea *model.StagingArea, highHash *externalapi.DomainHash) (
	[]*externalapi.DomainHash, error,
) {
	pruningPoint, err := sm.pruningStore.PruningPoint(sm.databaseContext, stagingArea)
	if err != nil {
		return nil, err
	}

	selectedChildIterator, err := sm.dagTraversalManager.SelectedChildIterator(stagingArea, highHash, pruningPoint, false)
	if err != nil {
		return nil, err
	}
	defer selectedChildIterator.Close()

	lowHash := pruningPoint
	foundHeaderOnlyBlock := false
	for ok := selectedChildIterator.First(); ok; ok = selectedChildIterator.Next() {
		selectedChild, err := selectedChildIterator.Get()
		if err != nil {
			return nil, err
		}
		blockStatus, err := sm.blockStatusStore.Get(sm.databaseContext, stagingArea, selectedChild)
		if database.IsNotFoundError(err) {
			log.Infof("missingBlockBodyHashes failed to retrieve with %s\n", selectedChild)
			return nil, err
		}
		if err != nil {
			return nil, err
		}
		if blockStatus == externalapi.StatusHeaderOnly {
			foundHeaderOnlyBlock = true
			break
		}
		lowHash = selectedChild
	}
	if !foundHeaderOnlyBlock {
		if lowHash.Equal(highHash) {
			// Blocks can be inserted inside the DAG during IBD if those were requested before IBD started.
			// In rare cases, all the IBD blocks might be already inserted by the time we reach this point.
			// In these cases - return an empty list of blocks to sync
			return []*externalapi.DomainHash{}, nil
		}
		// No header-only blocks found - this can cause incomplete IBD
		log.Errorf("No header-only blocks between %s and %s",
			lowHash, highHash)
		return nil, errors.Errorf("no header-only blocks found between %s and %s", lowHash, highHash)
	}

	hashesBetween, _, err := sm.antiPastHashesBetween(stagingArea, lowHash, highHash, 0)
	if err != nil {
		return nil, err
	}
	log.Infof("HashesBetween %d", len(hashesBetween))
	missingBlocks := make([]*externalapi.DomainHash, 0, len(hashesBetween))
	for _, blockHash := range hashesBetween {
		blockStatus, err := sm.blockStatusStore.Get(sm.databaseContext, stagingArea, blockHash)
		if err != nil {
			return nil, err
		}
		log.Infof("Missing block body status %s", blockStatus)
		if blockStatus == externalapi.StatusHeaderOnly {
			missingBlocks = append(missingBlocks, blockHash)
		}
	}

	return missingBlocks, nil
}
