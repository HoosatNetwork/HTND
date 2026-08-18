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
		log.Debugf("antiPastHashesBetween failed to retrieve low with %s\n", lowHash)
		return nil, nil, err
	}
	if err != nil {
		return nil, nil, err
	}
	highBlockGHOSTDAGData, err := sm.ghostdagDataStore.Get(sm.databaseContext, stagingArea, highHash, false)
	if database.IsNotFoundError(err) {
		log.Debugf("antiPastHashesBetween failed to retrieve high with %s\n", highHash)
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
	seen := make(map[externalapi.DomainHash]struct{})
	iterator, err := sm.dagTraversalManager.ChildIterator(stagingArea, highHash, lowHash, false)
	if err != nil {
		return nil, nil, err
	}
	// log.Infof("LowHash %s, HighHash %s", lowHash, highHash)
	defer iterator.Close()
	for ok := iterator.First(); ok; ok = iterator.Next() {
		current, err := iterator.Get()
		if err != nil {
			return nil, nil, err
		}

		total := len(blockHashes)
		if total < 0 {
			// Should never happen, but guard for safety
			break
		}
		if maxBlocks != 0 && uint64(total) > maxBlocks {
			break
		}

		highHash = current

		isInPastOfOriginalLowHash, err := sm.dagTopologyManager.IsAncestorOf(stagingArea, current, originalLowHash)
		if err != nil {
			return nil, nil, err
		}
		if isInPastOfOriginalLowHash {
			// log.Infof("Skipping %s sorted mergeset, because IsAncestorOf %s", current, originalLowHash)
			continue
		}
		sortedMergeSet, err := sm.ghostdagManager.GetSortedMergeSet(stagingArea, current)
		if err != nil {
			return nil, nil, err
		}
		for _, blockHash := range sortedMergeSet {
			isInPastOfOriginalLowHash, err := sm.dagTopologyManager.IsAncestorOf(stagingArea, blockHash, originalLowHash)
			if err != nil {
				return nil, nil, err
			}
			if isInPastOfOriginalLowHash {
				// log.Infof("Skipping %s on %s sorted mergeset, because IsAncestorOf %s", blockHash, current, originalLowHash)
				continue
			}
			if _, exists := seen[*blockHash]; !exists {
				seen[*blockHash] = struct{}{}
				blockHashes = append(blockHashes, blockHash)
			}
		}
		if _, exists := seen[*current]; !exists {
			seen[*current] = struct{}{}
			blockHashes = append(blockHashes, current)
		}
	}

	// The process above doesn't return highHash, so include it explicitly, unless highHash == lowHash
	if !lowHash.Equal(highHash) {
		if _, exists := seen[*highHash]; !exists {
			blockHashes = append(blockHashes, highHash)
		}
	}

	// for i, hash := range blockHashes {
	// 	log.Infof("%d, %s", i, hash)
	// }

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
		log.Debugf("originalLowHash %s changed to %s", originalLowHash, lowHash)
	}

	lowBlockGHOSTDAGData, err := sm.ghostdagDataStore.Get(sm.databaseContext, stagingArea, lowHash, false)
	if database.IsNotFoundError(err) {
		log.Debugf("antiPastHashesBetween failed to retrieve low with %s\n", lowHash)
		return nil, nil, err
	}
	if err != nil {
		return nil, nil, err
	}
	highBlockGHOSTDAGData, err := sm.ghostdagDataStore.Get(sm.databaseContext, stagingArea, highHash, false)
	if database.IsNotFoundError(err) {
		log.Debugf("antiPastHashesBetween failed to retrieve high with %s\n", highHash)
		return nil, nil, err
	}
	if err != nil {
		return nil, nil, err
	}
	if lowBlockGHOSTDAGData.BlueScore() > highBlockGHOSTDAGData.BlueScore() {
		return nil, nil, errors.Errorf("low hash blueScore > high hash blueScore (%d > %d)",
			lowBlockGHOSTDAGData.BlueScore(), highBlockGHOSTDAGData.BlueScore())
	}

	log.Debugf("Low %s, High %s", lowHash, highHash)
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
		log.Debugf("Current block %s", current)
		header, err := sm.blockHeaderStore.BlockHeader(sm.databaseContext, stagingArea, current)
		if err != nil {
			return nil, nil, err
		}
		parents3 := make([]*externalapi.DomainHash, 0)
		parents2 := make([]*externalapi.DomainHash, 0)
		parents1 := make([]*externalapi.DomainHash, 0)
		parents := make([]*externalapi.DomainHash, 0)

		for _, blockLevelParent := range header.Parents() {
			for _, parent := range blockLevelParent {
				if _, exists := seen[*parent]; !exists {
					seen[*parent] = struct{}{}
					parents = append(parents, parent)
				}
			}
		}

		for _, parent := range parents {
			header, err := sm.blockHeaderStore.BlockHeader(sm.databaseContext, stagingArea, parent)
			if err != nil {
				return nil, nil, err
			}
			for _, blockLevelParent := range header.Parents() {
				for _, parent := range blockLevelParent {
					if _, exists := seen[*parent]; !exists {
						seen[*parent] = struct{}{}
						parents1 = append(parents1, parent)
					}
				}
			}
		}
		for _, parent := range parents1 {
			header, err := sm.blockHeaderStore.BlockHeader(sm.databaseContext, stagingArea, parent)
			if err != nil {
				return nil, nil, err
			}
			for _, blockLevelParent := range header.Parents() {
				for _, parent := range blockLevelParent {
					if _, exists := seen[*parent]; !exists {
						seen[*parent] = struct{}{}
						parents2 = append(parents2, parent)
					}
				}
			}
		}
		for _, parent := range parents2 {
			header, err := sm.blockHeaderStore.BlockHeader(sm.databaseContext, stagingArea, parent)
			if err != nil {
				return nil, nil, err
			}
			for _, blockLevelParent := range header.Parents() {
				for _, parent := range blockLevelParent {
					if _, exists := seen[*parent]; !exists {
						seen[*parent] = struct{}{}
						parents3 = append(parents3, parent)
					}
				}
			}
		}

		// log.Debugf("Printing current block mergeset")
		// for i, blockhash := range parents {
		// 	log.Debugf("%d %s", i, blockhash)
		// }

		total := len(blockHashes) + len(parents)
		if total < 0 {
			// Should never happen, but guard for safety
			break
		}
		if maxBlocks != 0 && uint64(total) > maxBlocks {
			break
		}

		for _, blockHash := range parents3 {
			isInPastOfOriginalLowHash, err := sm.dagTopologyManager.IsAncestorOf(stagingArea, blockHash, originalLowHash)
			if err != nil {
				return nil, nil, err
			}
			if isInPastOfOriginalLowHash {
				log.Debugf("Dismissing %s from mergeset, parent of %s, because is in past of original original low hash %s", blockHash, current, originalLowHash)
				continue
			}
			blockHashes = append(blockHashes, blockHash)
		}

		for _, blockHash := range parents2 {
			isInPastOfOriginalLowHash, err := sm.dagTopologyManager.IsAncestorOf(stagingArea, blockHash, originalLowHash)
			if err != nil {
				return nil, nil, err
			}
			if isInPastOfOriginalLowHash {
				log.Debugf("Dismissing %s from mergeset, parent of %s, because is in past of original original low hash %s", blockHash, current, originalLowHash)
				continue
			}
			blockHashes = append(blockHashes, blockHash)
		}

		// append to blockHashes all blocks in sortedMergeSet which are not in the past of originalLowHash
		for _, blockHash := range parents1 {
			isInPastOfOriginalLowHash, err := sm.dagTopologyManager.IsAncestorOf(stagingArea, blockHash, originalLowHash)
			if err != nil {
				return nil, nil, err
			}
			if isInPastOfOriginalLowHash {
				log.Debugf("Dismissing %s from mergeset, parent of %s, because is in past of original original low hash %s", blockHash, current, originalLowHash)
				continue
			}
			blockHashes = append(blockHashes, blockHash)
		}

		for _, blockHash := range parents {
			isInPastOfOriginalLowHash, err := sm.dagTopologyManager.IsAncestorOf(stagingArea, blockHash, originalLowHash)
			if err != nil {
				return nil, nil, err
			}
			if isInPastOfOriginalLowHash {
				log.Debugf("Dismissing %s from mergeset, parent of %s, because is in past of original original low hash %s", blockHash, current, originalLowHash)
				continue
			}
			blockHashes = append(blockHashes, blockHash)
		}
	}

	// The process above doesn't return highHash, so include it explicitly, unless highHash == lowHash
	if !lowHash.Equal(highHash) {
		blockHashes = append(blockHashes, highHash)
	}
	blockHashes = hashset.NewFromSlice(blockHashes...).ToSlice()

	// Sort by blue score to get topological order
	if err := sm.sortByBlueScore(stagingArea, blockHashes); err != nil {
		return nil, nil, err
	}

	// log.Debugf("Printing current block hashes")
	// for i, blockhash := range blockHashes {
	// 	log.Debugf("%d %s", i, blockhash)
	// }
	return blockHashes, highHash, nil
}

// sortByBlueScore sorts a slice of block hashes by their blue score in ascending order.
// Blue score is monotonically increasing along any chain, so sorting by it
// ensures ancestors come before descendants, which is the definition of topological order.
func (sm *syncManager) sortByBlueScore(stagingArea *model.StagingArea, hashes []*externalapi.DomainHash) error {
	if len(hashes) <= 1 {
		return nil
	}

	// Use bubble sort for simplicity (the slice is typically small)
	swapped := true
	for swapped {
		swapped = false
		for i := 0; i < len(hashes)-1; i++ {
			if hashes[i].Equal(hashes[i+1]) {
				continue
			}
			iScore, err := sm.getBlueScore(stagingArea, hashes[i])
			if err != nil {
				return err
			}
			jScore, err := sm.getBlueScore(stagingArea, hashes[i+1])
			if err != nil {
				return err
			}
			if iScore > jScore {
				hashes[i], hashes[i+1] = hashes[i+1], hashes[i]
				swapped = true
			}
		}
	}
	return nil
}

// getBlueScore is a helper function to get the blue score of a block
func (sm *syncManager) getBlueScore(stagingArea *model.StagingArea, hash *externalapi.DomainHash) (uint64, error) {
	ghostdagData, err := sm.ghostdagDataStore.Get(sm.databaseContext, stagingArea, hash, false)
	if err != nil {
		return 0, err
	}
	return ghostdagData.BlueScore(), nil
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
			log.Debugf("findLowHashInHighHashSelectedParentChain failed to retrieve with %s\n", lowHash)
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
			log.Debugf("missingBlockBodyHashes failed to retrieve with %s\n", selectedChild)
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
	log.Debugf("HashesBetween %d", len(hashesBetween))
	missingBlocks := make([]*externalapi.DomainHash, 0, len(hashesBetween))
	for _, blockHash := range hashesBetween {
		blockStatus, err := sm.blockStatusStore.Get(sm.databaseContext, stagingArea, blockHash)
		if err != nil {
			return nil, err
		}
		log.Debugf("Missing block body status %s", blockStatus)
		if blockStatus == externalapi.StatusHeaderOnly {
			missingBlocks = append(missingBlocks, blockHash)
		}
	}

	return missingBlocks, nil
}
