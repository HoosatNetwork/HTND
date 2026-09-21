package dagtraversalmanager

import (
	"slices"

	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/blockversion"
	"github.com/HoosatNetwork/HTND/infrastructure/db/database"
)

// DAABlockWindow returns highHash's DAA window, sized for highHash's own block version rather than the process-global
// version: the window is served to IBD peers as trusted data, and must not depend on the serving node's uptime.
func (dtm *dagTraversalManager) DAABlockWindow(stagingArea *model.StagingArea, highHash *externalapi.DomainHash) ([]*externalapi.DomainHash, error) {
	blockVersion, err := blockversion.OfSelectedParent(dtm.databaseContext, stagingArea, dtm.ghostdagDataStore,
		dtm.daaBlocksStore, dtm.powScores, highHash)
	if err != nil {
		return nil, err
	}
	windowSize := dtm.difficultyAdjustmentWindowSize[blockversion.Index(blockVersion, len(dtm.difficultyAdjustmentWindowSize))]
	return dtm.BlockWindow(stagingArea, highHash, windowSize)
}

// BlockWindowHeapSlice returns the cached or computed heap slice for the given
// block window. The returned slice must be treated as read-only by callers.
func (dtm *dagTraversalManager) BlockWindowHeapSlice(stagingArea *model.StagingArea, highHash *externalapi.DomainHash,
	windowSize int, includeTrustedWindow bool,
) ([]*externalapi.BlockGHOSTDAGDataHashPair, error) {
	// Fast path: if the heap slice is already cached in the staging-area-aware
	// store, return it directly without cloning or extracting a hash-only view.
	cachedSlice, err := dtm.windowHeapSliceStore.Get(stagingArea, highHash, windowSize, includeTrustedWindow)
	if err == nil {
		return cachedSlice, nil
	}
	if !database.IsNotFoundError(err) {
		return nil, err
	}

	// Slow path: full computation (cache miss)
	windowHeap, err := dtm.calculateBlockWindowHeap(stagingArea, highHash, windowSize, includeTrustedWindow)
	if err != nil {
		return nil, err
	}

	if !highHash.Equal(model.VirtualBlockHash) {
		dtm.windowHeapSliceStore.Stage(stagingArea, highHash, windowSize, includeTrustedWindow, windowHeap.impl.slice)
	}

	return windowHeap.impl.slice, nil
}

// BlockWindow returns a blockWindow of the given size that contains the
// blocks in the past of highHash, the sorting is unspecified.
// If the number of blocks in the past of startingNode is less then windowSize,
func (dtm *dagTraversalManager) BlockWindow(stagingArea *model.StagingArea, highHash *externalapi.DomainHash,
	windowSize int,
) ([]*externalapi.DomainHash, error) {
	// includeTrustedWindow is false here on purpose, and this is the whole of HTN-204's containment.
	//
	// BlockWindow has three callers and none of them may walk into a pruned block's trusted window:
	//   - DAABlockWindow, which is what this node SERVES to a syncing peer as trusted data. It must
	//     only ever name blocks this node can actually answer TrustedDataDataDAAHeader for.
	//   - pastMedianTimeManager, which feeds validateMedianTime - a check that IS enabled, unlike
	//     the difficulty one, so changing it would change which blocks this node accepts.
	//   - pruningManager.blocksToKeep, which deliberately mirrors "the window DAABlockWindow serves
	//     for it" when deciding what to retain.
	//
	// Only difficultyManager wants the trusted window, and it asks for it directly.
	windowHeapSlice, err := dtm.BlockWindowHeapSlice(stagingArea, highHash, windowSize, false)
	if err != nil {
		return nil, err
	}

	window := make([]*externalapi.DomainHash, len(windowHeapSlice))
	for i, b := range windowHeapSlice {
		window[i] = b.Hash
	}

	return window, nil
}

func (dtm *dagTraversalManager) calculateBlockWindowHeap(stagingArea *model.StagingArea,
	highHash *externalapi.DomainHash, windowSize int, includeTrustedWindow bool,
) (*sizedUpBlockHeap, error) {
	if highHash.Equal(dtm.genesisHash) {
		return dtm.newSizedUpHeap(stagingArea, windowSize), nil
	}
	if windowSize == 0 {
		return dtm.newSizedUpHeap(stagingArea, windowSize), nil
	}

	current := highHash
	currentGHOSTDAGData, err := dtm.ghostdagDataStore.Get(dtm.databaseContext, stagingArea, highHash, false)
	if database.IsNotFoundError(err) {
		log.Infof("calculateBlockWindowHeap failed to retrieve with %s\n", highHash)
		return nil, err
	}
	if err != nil {
		return nil, err
	}

	// If the block has a trusted DAA window attached, we just take it as is and don't use cache of selected parent to
	// build the window. This is because tryPushMergeSet might not be able to find all the GHOSTDAG data that is
	// associated with the block merge set.
	_, err = dtm.daaWindowStore.DAAWindowBlock(dtm.databaseContext, stagingArea, current, 0)
	isNonTrustedBlock := database.IsNotFoundError(err)
	if !isNonTrustedBlock && err != nil {
		return nil, err
	}

	if isNonTrustedBlock && currentGHOSTDAGData.SelectedParent() != nil {
		windowHeapSlice, err := dtm.windowHeapSliceStore.Get(stagingArea, currentGHOSTDAGData.SelectedParent(), windowSize, includeTrustedWindow)
		selectedParentNotCached := database.IsNotFoundError(err)
		if !selectedParentNotCached && err != nil {
			return nil, err
		}
		if !selectedParentNotCached {
			windowHeap := dtm.newSizedUpHeapFromSlice(stagingArea, windowHeapSlice)
			if !currentGHOSTDAGData.SelectedParent().Equal(dtm.genesisHash) {
				selectedParentGHOSTDAGData, err := dtm.ghostdagDataStore.Get(
					dtm.databaseContext, stagingArea, currentGHOSTDAGData.SelectedParent(), false)
				if err != nil {
					return nil, err
				}

				_, err = dtm.tryPushMergeSet(windowHeap, currentGHOSTDAGData, selectedParentGHOSTDAGData)
				if err != nil {
					return nil, err
				}
			}

			return windowHeap, nil
		}
	}

	windowHeap := dtm.newSizedUpHeap(stagingArea, windowSize)
	// Walk down the chain until you finish or find a trusted block and then take complete the rest
	// of the window with the trusted window.
	for {
		selectedParent := currentGHOSTDAGData.SelectedParent()
		if selectedParent.Equal(nil) {
			break
		}
		if selectedParent.Equal(dtm.genesisHash) {
			break
		}

		// HTN-204. The virtual-genesis marker is what a pruned selected parent is replaced with by
		// validateAndInsertBlockWithTrustedData, so the pruning point is precisely the block whose
		// selected parent is this marker AND whose window exists only as trusted data.
		//
		// Breaking here - before the daaWindowStore lookup below - means the walk stops one
		// statement before the data it came for, and the pruning point's window comes back empty.
		// BlockWindowHeapSlice then caches that, every child builds from its selected parent's
		// cached slice, and a freshly synced node mines at genesis difficulty until a full window of
		// new blocks accumulates.
		//
		// Only the difficulty path may walk through into the trusted window. The serving path must
		// not: it would then enumerate blocks this node has no trusted data for, and answering a
		// peer's pruning-point-anticone request with them kills that peer's IBD. That failure is
		// what sank the original fix, reproduced 3/3 for docs/design/HTN-204.md.
		if !includeTrustedWindow && selectedParent.Equal(model.VirtualGenesisBlockHash) {
			break
		}

		_, err := dtm.daaWindowStore.DAAWindowBlock(dtm.databaseContext, stagingArea, current, 0)
		currentIsNonTrustedBlock := database.IsNotFoundError(err)
		if !currentIsNonTrustedBlock && err != nil {
			return nil, err
		}

		if !currentIsNonTrustedBlock {
			for i := uint64(0); ; i++ {
				daaBlock, err := dtm.daaWindowStore.DAAWindowBlock(dtm.databaseContext, stagingArea, current, i)
				if database.IsNotFoundError(err) {
					break
				}
				if err != nil {
					return nil, err
				}

				_, err = windowHeap.tryPushWithGHOSTDAGData(daaBlock.Hash, daaBlock.GHOSTDAGData)
				if err != nil {
					return nil, err
				}

				// Right now we go over all of the window of `current` and filter blocks on the fly.
				// We can optimize it if we make sure that daaWindowStore stores sorted windows, and
				// then return from this function once one block was not added to the heap.
			}
			break
		}

		// The guard the break above used to provide, kept where it is still needed: the marker is
		// not a real block, so looking up its GHOSTDAG data would fail. Reached only when
		// includeTrustedWindow is set and this block carried no trusted window after all.
		if selectedParent.Equal(model.VirtualGenesisBlockHash) {
			break
		}

		selectedParentGHOSTDAGData, err := dtm.ghostdagDataStore.Get(
			dtm.databaseContext, stagingArea, selectedParent, false)
		if err != nil {
			return nil, err
		}

		done, err := dtm.tryPushMergeSet(windowHeap, currentGHOSTDAGData, selectedParentGHOSTDAGData)
		if err != nil {
			return nil, err
		}
		if done {
			break
		}

		current = selectedParent
		currentGHOSTDAGData = selectedParentGHOSTDAGData
	}

	return windowHeap, nil
}

func (dtm *dagTraversalManager) tryPushMergeSet(windowHeap *sizedUpBlockHeap, currentGHOSTDAGData, selectedParentGHOSTDAGData *externalapi.BlockGHOSTDAGData) (bool, error) {
	added, err := windowHeap.tryPushWithGHOSTDAGData(currentGHOSTDAGData.SelectedParent(), selectedParentGHOSTDAGData)
	if err != nil {
		return false, err
	}

	// If the window is full and the selected parent is less than the minimum then we break
	// because this means that there cannot be any more blocks in the past with higher blueWork
	if !added {
		return true, nil
	}

	// Now we go over the merge set.
	// Remove the SP from the blue merge set because we already added it.
	mergeSetBlues := currentGHOSTDAGData.MergeSetBlues()[1:]
	// Go over the merge set in reverse because it's ordered in reverse by blueWork.
	for _, mergeSetBlue := range slices.Backward(mergeSetBlues) {
		added, err := windowHeap.tryPush(mergeSetBlue)
		if err != nil {
			return false, err
		}
		// If it's smaller than minimum then we won't be able to add the rest because they're even smaller.
		if !added {
			break
		}
	}

	mergeSetReds := currentGHOSTDAGData.MergeSetReds()
	for _, mergeSetRed := range slices.Backward(mergeSetReds) {
		added, err := windowHeap.tryPush(mergeSetRed)
		if err != nil {
			return false, err
		}
		// If it's smaller than minimum then we won't be able to add the rest because they're even smaller.
		if !added {
			break
		}
	}

	return false, nil
}
