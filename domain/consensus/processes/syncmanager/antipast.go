package syncmanager

import (
	"sort"

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

	// Collect all hashes by concatenating the merge sets of every chain block between lowHash and
	// highHash.
	//
	// The traversal below is a BFS over children, which is NOT a topological order: a block can be
	// dequeued before another path to one of its own parents has been explored. The batch is
	// therefore sorted before it is returned.
	//
	// That sort must key on blue WORK. The receiving peer inserts headers in the order they arrive
	// and rejects any block whose parents it hasn't seen yet with ErrMissingParents, aborting the
	// whole IBD. Blue work is strictly increasing from any parent to its child - a block's selected
	// parent is its maximum-blue-work parent, and the block's own blue work is that plus the work of
	// every blue block it merges - so ordering by it can never place a block before an ancestor.
	//
	// This previously sorted by blue SCORE, which has no such property: blue score counts blue
	// blocks rather than accumulating their difficulty, so a parent on a long low-difficulty branch
	// can outscore the child that merges it. Such a pair was emitted child-first and the peer
	// rejected it. It only misfires when a diverged-difficulty pair lands in the same batch, which
	// is why it hit some nodes and not others.
	blockHashes := []*externalapi.DomainHash{}
	seen := make(map[externalapi.DomainHash]struct{})
	actualHighHash = lowHash

	iterator, err := sm.dagTraversalManager.ChildIterator(stagingArea, highHash, lowHash, false)
	if err != nil {
		return nil, nil, err
	}
	defer iterator.Close()

	for ok := iterator.First(); ok; ok = iterator.Next() {
		current, err := iterator.Get()
		if err != nil {
			return nil, nil, err
		}

		// Budget check before starting another merge set, so a batch overshoots by at most one
		// merge set (which is why maxBlocks must be >= mergeSetSizeLimit + 1).
		if maxBlocks != 0 && uint64(len(blockHashes)) > maxBlocks {
			break
		}

		// actualHighHash is handed back to the caller as next round's lowHash. It must only ever
		// be a block that's actually on highHash's selected parent chain (or highHash itself):
		// the ChildIterator's BFS visits any child that's an ancestor of highHash, which includes
		// off-chain merge-set blocks that are NOT on the chain. If actualHighHash were set to one
		// of those, the next call's findLowHashInHighHashSelectedParentChain would slide it
		// backward to the nearest chain ancestor instead of resuming forward - re-serving the same
		// window forever instead of making progress. Off-chain blocks still get included in
		// blockHashes below; they just can't be used as the resume checkpoint.
		isCurrentOnChain, err := sm.dagTopologyManager.IsInSelectedParentChainOf(stagingArea, current, highHash)
		if err != nil {
			return nil, nil, err
		}

		isInPastOfOriginalLowHash, err := sm.dagTopologyManager.IsAncestorOf(stagingArea, current, originalLowHash)
		if err != nil {
			return nil, nil, err
		}
		if isInPastOfOriginalLowHash {
			// The peer already has this block and its whole past; skip its merge set but still
			// advance the checkpoint, so a run of already-known chain blocks can't stall the batch.
			if isCurrentOnChain || current.Equal(highHash) {
				actualHighHash = current
			}
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

		if isCurrentOnChain || current.Equal(highHash) {
			actualHighHash = current
		}
	}

	// BFS order is not topological - sort before handing the batch to the peer.
	if err := sm.sortInTopologicalOrder(stagingArea, blockHashes); err != nil {
		return nil, nil, err
	}

	return blockHashes, actualHighHash, nil
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

	// Sort into topological order. The receiving peer inserts headers in the order they arrive and
	// rejects any block whose parents it hasn't seen yet, so a block must never precede an ancestor.
	if err := sm.sortInTopologicalOrder(stagingArea, blockHashes); err != nil {
		return nil, nil, err
	}

	// log.Debugf("Printing current block hashes")
	// for i, blockhash := range blockHashes {
	// 	log.Debugf("%d %s", i, blockhash)
	// }
	return blockHashes, highHash, nil
}

// sortInTopologicalOrder sorts a slice of block hashes so that every block comes after all of its
// ancestors.
//
// The key is blue WORK, not blue score. Blue work strictly increases from any parent to its child: a
// block's selected parent is its maximum-blue-work parent, and the block's own blue work is its
// selected parent's plus the work of every blue block it merges, so
// blueWork(child) > blueWork(selectedParent) >= blueWork(anyOtherParent). By transitivity that holds
// for every ancestor, which is exactly the guarantee a syncing peer needs.
//
// Blue score does NOT have this property. It counts blue blocks instead of accumulating work, so a
// branch of many low-difficulty blocks can give a parent a HIGHER blue score than the child that
// merges it. Sorting by blue score emits such a pair in the wrong order and the receiving peer
// rejects the child with ErrMissingParents, aborting the IBD - which is why this used to fail on
// some nodes and not others, depending on which merge sets landed in a batch.
//
// Ties are broken by hash, matching ghostdagManager.Less, so the order is total and identical on
// every node.
func (sm *syncManager) sortInTopologicalOrder(stagingArea *model.StagingArea, hashes []*externalapi.DomainHash) error {
	if len(hashes) <= 1 {
		return nil
	}

	// Fetch each block's GHOSTDAG data once up front. The sort makes O(n log n) comparisons and each
	// one would otherwise be a store lookup.
	ghostdagData := make(map[externalapi.DomainHash]*externalapi.BlockGHOSTDAGData, len(hashes))
	for _, hash := range hashes {
		if _, exists := ghostdagData[*hash]; exists {
			continue
		}
		data, err := sm.ghostdagDataStore.Get(sm.databaseContext, stagingArea, hash, false)
		if err != nil {
			return err
		}
		ghostdagData[*hash] = data
	}

	sort.Slice(hashes, func(i, j int) bool {
		return sm.ghostdagManager.Less(hashes[i], ghostdagData[*hashes[i]], hashes[j], ghostdagData[*hashes[j]])
	})

	return nil
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

	// SelectedChildIterator requires its low anchor to be on highHash's selected parent chain, and
	// normally the pruning point always is. On this network it sometimes isn't - the canonical
	// pruning-point / chain data served by peers doesn't fully self-reconcile (the same condition
	// that makes an imported pruning-point UTXO set fail its own header commitment), so the pruning
	// point this node settled on can sit off the chain highHash's selected chain passes through.
	// This is hit even on a completely fresh sync, so it must not fail IBD. Fall back to the deepest
	// ancestor of the pruning point that IS on highHash's selected chain (the same slide
	// antiPastHashesBetween does internally below) and sync bodies from there; if even that can't be
	// established, return an empty result so IBD still completes and the node tracks the tip via
	// block relay.
	lowAnchor := pruningPoint
	isPruningPointInHighHashChain, err := sm.dagTopologyManager.IsInSelectedParentChainOf(stagingArea, pruningPoint, highHash)
	if err != nil {
		log.Warnf("missingBlockBodyHashes: could not check whether pruning point %s is on %s's selected "+
			"parent chain (%s) - skipping body sync for this segment", pruningPoint, highHash, err)
		return []*externalapi.DomainHash{}, nil
	}
	if !isPruningPointInHighHashChain {
		lowAnchor, err = sm.findLowHashInHighHashSelectedParentChain(stagingArea, pruningPoint, highHash)
		if err != nil {
			log.Warnf("missingBlockBodyHashes: pruning point %s is not on %s's selected parent chain and no "+
				"shared ancestor is reachable within available data (%s) - the network's pruning-point/chain "+
				"data does not fully reconcile here; skipping body sync for this segment", pruningPoint, highHash, err)
			return []*externalapi.DomainHash{}, nil
		}
		log.Warnf("missingBlockBodyHashes: pruning point %s is not on %s's selected parent chain (the "+
			"network's pruning-point/chain data does not fully reconcile here) - syncing bodies from shared "+
			"ancestor %s", pruningPoint, highHash, lowAnchor)
	}

	selectedChildIterator, err := sm.dagTraversalManager.SelectedChildIterator(stagingArea, highHash, lowAnchor, false)
	if err != nil {
		log.Warnf("missingBlockBodyHashes: could not build selected-child iterator from %s to %s (%s) - "+
			"skipping body sync for this segment", lowAnchor, highHash, err)
		return []*externalapi.DomainHash{}, nil
	}
	defer selectedChildIterator.Close()

	lowHash := lowAnchor
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
		// No header-only block was reached along the walk even though lowHash != highHash. On a
		// fully self-consistent chain this shouldn't happen; here it just means there is nothing on
		// this segment we can pull bodies for. Return empty rather than failing IBD.
		log.Warnf("missingBlockBodyHashes: no header-only blocks between %s and %s - skipping body sync "+
			"for this segment", lowHash, highHash)
		return []*externalapi.DomainHash{}, nil
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
