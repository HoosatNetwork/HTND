package consensusstatemanager

import (
	"github.com/HoosatNetwork/HTND/v2/infrastructure/logger"
	"github.com/HoosatNetwork/HTND/v2/util/math"
	"github.com/pkg/errors"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/database"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/hashset"
)

func (csm *consensusStateManager) pickVirtualParents(stagingArea *model.StagingArea, tips []*externalapi.DomainHash) ([]*externalapi.DomainHash, error) {
	onEnd := logger.LogAndMeasureExecutionTime(log, "pickVirtualParents")
	defer onEnd()

	log.Debugf("pickVirtualParents start for tips len: %d", len(tips))

	log.Debugf("Pushing all tips into a DownHeap")
	candidatesHeap := csm.dagTraversalManager.NewDownHeap(stagingArea)
	for _, tip := range tips {
		err := candidatesHeap.Push(tip)
		if err != nil {
			return nil, err
		}
	}

	// If the first candidate has been disqualified from the chain or violates finality -
	// it cannot be virtual's parent, since it will make it virtual's selectedParent - disqualifying virtual itself.
	// Therefore, in such a case we remove it from the list of virtual parent candidates, and replace with
	// its parents that have no disqualified children
	virtualSelectedParent, err := csm.selectVirtualSelectedParent(stagingArea, candidatesHeap)
	if err != nil {
		return nil, err
	}
	log.Debugf("The selected parent of the virtual is: %s", virtualSelectedParent)

	// The parents limit of the block these parents will be mined into, not of the process-global version: header
	// validation applies the block's own version's limit, so a larger global limit built templates nobody accepts.
	nextBlockVersion, err := csm.versionOfChildOf(stagingArea, virtualSelectedParent)
	if err != nil {
		return nil, err
	}
	maxBlockParents := csm.maxBlockParentsForVersion(nextBlockVersion)

	// Limit to maxBlockParents*3 candidates, that way we don't go over thousands of tips when the network isn't healthy.
	// There's no specific reason for a factor of 3, and its not a consensus rule, just an estimation saying we probably
	// don't want to consider and calculate 3 times the amount of candidates for the set of parents.
	maxCandidates := int(maxBlockParents) * 3
	candidateAllocationSize := math.MinInt(maxCandidates, candidatesHeap.Len())
	candidates := make([]*externalapi.DomainHash, 0, candidateAllocationSize)
	for len(candidates) < maxCandidates && candidatesHeap.Len() > 0 {
		candidates = append(candidates, candidatesHeap.Pop())
	}

	// prioritize half the blocks with highest blueWork and half with lowest, so the network will merge splits faster.
	if len(candidates) >= int(maxBlockParents) {
		// We already have the selectedParent, so we're left with csm.maxBlockParents-1.
		maxParents := maxBlockParents - 1
		end := len(candidates) - 1
		for i := (maxParents) / 2; i < maxParents; i++ {
			candidates[i], candidates[end] = candidates[end], candidates[i]
			end--
		}
	}

	selectedVirtualParents := []*externalapi.DomainHash{virtualSelectedParent}
	mergeSetSize := uint64(1) // starts counting from 1 because selectedParent is already in the mergeSet

	// knownPastOfSelectedVirtualParents memoizes IsAncestorOfAny(x, selectedVirtualParents) == true
	// results across every mergeSetIncrease call in the loop below. This is safe because
	// selectedVirtualParents only ever grows in this loop (a candidate is appended to it, never
	// removed): once x is known to be an ancestor of some member of the set, it stays an ancestor of
	// that same member for any later, larger set, so a "true" answer never needs to be recomputed. A
	// "false" answer is deliberately NOT cached here, since it can flip to true once a later
	// candidate is added to selectedVirtualParents - only "true" is monotonic.
	//
	// Different candidate tips' merge-set BFS (inside mergeSetIncrease) walk the same shared DAG and
	// visit heavily overlapping ancestors on a DAG with many tips - a live CPU profile showed this
	// exact reachability check (IsAncestorOfAny, called once per BFS-visited node) as the largest
	// single cost in block processing while nearly synced. See HTN-215.
	knownPastOfSelectedVirtualParents := hashset.New()

	// First condition implies that no point in searching since limit was already reached
	for mergeSetSize < csm.mergeSetSizeLimit && len(candidates) > 0 && uint64(len(selectedVirtualParents)) < uint64(maxBlockParents) {
		candidate := candidates[0]
		candidates = candidates[1:]

		log.Debugf("Attempting to add %s to the virtual parents", candidate)
		log.Debugf("The current merge set size is %d", mergeSetSize)

		// mergeSetIncrease starts from the candidate's parents on the documented assumption that the
		// candidate itself is not an ancestor of the selected parents. A body synced for a block whose
		// children are still header-only becomes a tip even though it is buried in the selected
		// parent's past. Treating it as a parent makes every such body rerun GHOSTDAG and the UTXO
		// rebuild, and the template built from those parents fails checkParentsIncest
		// (ErrInvalidParentsRelation). It is already merged. Skip the walk.
		candidateInPast := knownPastOfSelectedVirtualParents.Contains(candidate)
		if !candidateInPast {
			candidateInPast, err = csm.dagTopologyManager.IsAncestorOfAny(stagingArea, candidate, selectedVirtualParents)
			if err != nil {
				return nil, err
			}
			if candidateInPast {
				knownPastOfSelectedVirtualParents.Add(candidate)
			}
		}
		if candidateInPast {
			log.Debugf("Skipping block %s because it is in the past of the selected virtual parents", candidate)
			continue
		}

		canBeParent, newCandidate, mergeSetIncrease, err := csm.mergeSetIncrease(
			stagingArea, candidate, selectedVirtualParents, mergeSetSize, knownPastOfSelectedVirtualParents)
		if err != nil {
			return nil, err
		}
		if canBeParent {
			mergeSetSize += mergeSetIncrease
			selectedVirtualParents = append(selectedVirtualParents, candidate)
			log.Tracef("Added block %s to the virtual parents set", candidate)
			continue
		}
		// If we already have a candidate in the past of newCandidate then skip.
		isInFutureOfCandidates, err := csm.dagTopologyManager.IsAnyAncestorOf(stagingArea, candidates, newCandidate)
		if err != nil {
			return nil, err
		}
		if isInFutureOfCandidates {
			continue
		}
		// Remove all candidates in the future of newCandidate
		candidates, err = csm.removeHashesInFutureOf(stagingArea, candidates, newCandidate)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, newCandidate)
		log.Debugf("Block %s increases merge set too much, instead adding its ancestor %s", candidate, newCandidate)
	}

	// The stored virtual parents are the result of this function's previous call. Reaching the same
	// set again means bounded-merge GHOSTDAG would recolor virtual identically and then remove
	// nothing further. Return the stored parents so updateVirtual can skip the UTXO rebuild, and so
	// this call does not pay for that GHOSTDAG either.
	currentVirtualParents, currentParentsErr := csm.dagTopologyManager.Parents(stagingArea, model.VirtualBlockHash)
	if currentParentsErr != nil && !database.IsNotFoundError(currentParentsErr) {
		return nil, currentParentsErr
	}
	if currentParentsErr == nil && sameParentSet(currentVirtualParents, selectedVirtualParents) {
		log.Debugf("Virtual parent set unchanged (%d parents), skipping bounded-merge GHOSTDAG", len(currentVirtualParents))
		return currentVirtualParents, nil
	}

	boundedMergeBreakingParents, err := csm.boundedMergeBreakingParents(stagingArea, selectedVirtualParents)
	if err != nil {
		return nil, err
	}
	// At Info, not Trace: dropping a parent here is the node quietly declining to merge a branch that
	// bounded merge depth would reject, which permanently orphans every block on it. That is the right
	// call, but it is not a detail - when it fires repeatedly it is the only in-log evidence that a
	// branch has been abandoned, and when it fails to fire the node mines blocks it then rejects
	// itself with ErrViolatingBoundedMergeDepth and nothing says why.
	if len(boundedMergeBreakingParents) > 0 {
		log.Debugf("Omitting %d of %d candidate virtual parents for breaking the bounded merge set: %s",
			len(boundedMergeBreakingParents), len(selectedVirtualParents), boundedMergeBreakingParents)
	}

	// Remove all boundedMergeBreakingParents from selectedVirtualParents
	for _, breakingParent := range boundedMergeBreakingParents {
		for i, parent := range selectedVirtualParents {
			if parent.Equal(breakingParent) {
				selectedVirtualParents[i] = selectedVirtualParents[len(selectedVirtualParents)-1]
				selectedVirtualParents = selectedVirtualParents[:len(selectedVirtualParents)-1]
				break
			}
		}
	}
	log.Debugf("The virtual parents resolved to be: %s", selectedVirtualParents)
	return selectedVirtualParents, nil
}

func (csm *consensusStateManager) removeHashesInFutureOf(stagingArea *model.StagingArea, hashes []*externalapi.DomainHash,
	ancestor *externalapi.DomainHash,
) ([]*externalapi.DomainHash, error) {
	// Source: https://github.com/golang/go/wiki/SliceTricks#filter-in-place
	i := 0
	for _, hash := range hashes {
		isInFutureOfAncestor, err := csm.dagTopologyManager.IsAncestorOf(stagingArea, ancestor, hash)
		if err != nil {
			return nil, err
		}
		if !isInFutureOfAncestor {
			hashes[i] = hash
			i++
		}
	}
	return hashes[:i], nil
}

func (csm *consensusStateManager) selectVirtualSelectedParent(stagingArea *model.StagingArea,
	candidatesHeap model.BlockHeap,
) (*externalapi.DomainHash, error) {
	onEnd := logger.LogAndMeasureExecutionTime(log, "selectVirtualSelectedParent")
	defer onEnd()

	disqualifiedCandidates := hashset.New()

	// Hot-path optimizations:
	// 1. Cache status lookups to avoid repeating DB reads for the same hashes.
	// 2. For each parent, cache the number of relevant children (non-virtual, non-header-only).
	// 3. Track per-parent disqualified-child counts and push a parent once its relevant children are all disqualified.
	statusCache := make(map[externalapi.DomainHash]externalapi.BlockStatus)
	relevantChildCountCache := make(map[externalapi.DomainHash]uint32)
	disqualifiedChildCount := make(map[externalapi.DomainHash]uint32)
	pushedToHeap := make(map[externalapi.DomainHash]struct{})
	processed := make(map[externalapi.DomainHash]struct{})

	getStatus := func(hash *externalapi.DomainHash) (externalapi.BlockStatus, error) {
		key := *hash
		if status, ok := statusCache[key]; ok {
			return status, nil
		}
		status, err := csm.blockStatusStore.Get(csm.databaseContext, stagingArea, hash)
		if err != nil {
			return 0, err
		}
		statusCache[key] = status
		return status, nil
	}

	getRelevantChildCount := func(parent *externalapi.DomainHash) (uint32, error) {
		key := *parent
		if count, ok := relevantChildCountCache[key]; ok {
			return count, nil
		}

		allChildren, err := csm.dagTopologyManager.Children(stagingArea, parent)
		if err != nil {
			return 0, err
		}

		var count uint32
		for _, child := range allChildren {
			if child.Equal(model.VirtualBlockHash) {
				continue
			}
			childStatus, err := getStatus(child)
			if err != nil {
				return 0, err
			}
			if childStatus == externalapi.StatusHeaderOnly || childStatus == externalapi.StatusUTXOPendingVerification {
				continue
			}
			count++
		}
		relevantChildCountCache[key] = count
		return count, nil
	}

	for {
		if candidatesHeap.Len() == 0 {
			return nil, errors.New("virtual has no valid parent candidates")
		}
		selectedParentCandidate := candidatesHeap.Pop()

		// Skip candidate if it has already been processed in this pass
		candidateKey := *selectedParentCandidate
		if _, ok := processed[candidateKey]; ok {
			continue
		}
		processed[candidateKey] = struct{}{}

		selectedParentCandidateStatus, err := getStatus(selectedParentCandidate)
		if err != nil {
			return nil, err
		}
		if selectedParentCandidateStatus == externalapi.StatusUTXOValid {
			return selectedParentCandidate, nil
		}

		// Header-only and UTXO-pending-verification blocks are not considered for the
		// "all children disqualified" rule, so we don't mark them as disqualified or
		// increment disqualified child counts. However, we still need to consider their
		// parents as candidates.
		if selectedParentCandidateStatus == externalapi.StatusHeaderOnly || selectedParentCandidateStatus == externalapi.StatusUTXOPendingVerification {
			candidateParents, err := csm.dagTopologyManager.Parents(stagingArea, selectedParentCandidate)
			if err != nil {
				return nil, err
			}
			for _, parent := range candidateParents {
				if parent.Equal(model.VirtualBlockHash) {
					continue
				}
				parentKey := *parent
				if _, ok := pushedToHeap[parentKey]; ok {
					continue
				}
				pushedToHeap[parentKey] = struct{}{}
				err = candidatesHeap.Push(parent)
				if err != nil {
					return nil, err
				}
			}
			continue
		}

		disqualifiedCandidates.Add(selectedParentCandidate)

		candidateParents, err := csm.dagTopologyManager.Parents(stagingArea, selectedParentCandidate)
		if err != nil {
			return nil, err
		}
		for _, parent := range candidateParents {
			if parent.Equal(model.VirtualBlockHash) {
				continue
			}

			relevantChildrenCount, err := getRelevantChildCount(parent)
			if err != nil {
				return nil, err
			}

			parentKey := *parent
			disqualifiedChildCount[parentKey]++
			if disqualifiedChildCount[parentKey] < relevantChildrenCount {
				continue
			}
			if _, ok := pushedToHeap[parentKey]; ok {
				continue
			}
			pushedToHeap[parentKey] = struct{}{}

			err = candidatesHeap.Push(parent)
			if err != nil {
				return nil, err
			}
		}
	}
}

// mergeSetIncrease returns different things depending on the result:
// If the candidate can be a virtual parent then canBeParent=true and mergeSetIncrease=The increase in merge set size
// If the candidate can't be a virtual parent, then canBeParent=false and newCandidate is a new proposed candidate in the past of candidate.
//
// knownPastOfSelectedVirtualParents is shared across every candidate considered in the same
// pickVirtualParents call - see its doc comment there for why caching only "is an ancestor" (never
// "is not") across candidates is safe.
func (csm *consensusStateManager) mergeSetIncrease(stagingArea *model.StagingArea, candidate *externalapi.DomainHash,
	selectedVirtualParents []*externalapi.DomainHash, mergeSetSize uint64, knownPastOfSelectedVirtualParents hashset.HashSet) (
	canBeParent bool, newCandidate *externalapi.DomainHash, mergeSetIncrease uint64, err error,
) {
	onEnd := logger.LogAndMeasureExecutionTime(log, "mergeSetIncrease")
	defer onEnd()

	visited := hashset.New()
	// Start with the candidate's parents in the queue as we already know the candidate isn't an ancestor of the selectedVirtualParents.
	parents, err := csm.dagTopologyManager.Parents(stagingArea, candidate)
	if err != nil {
		return false, nil, 0, err
	}
	for _, parent := range parents {
		visited.Add(parent)
	}
	queue := parents
	mergeSetIncrease = uint64(1) // starts with 1 for the candidate itself

	var current *externalapi.DomainHash
	for len(queue) > 0 {
		current, queue = queue[0], queue[1:]
		log.Tracef("Attempting to increment the merge set size increase for block %s", current)

		isInPastOfSelectedVirtualParents := knownPastOfSelectedVirtualParents.Contains(current)
		if !isInPastOfSelectedVirtualParents {
			isInPastOfSelectedVirtualParents, err = csm.dagTopologyManager.IsAncestorOfAny(stagingArea, current, selectedVirtualParents)
			if err != nil {
				return false, nil, 0, err
			}
			if isInPastOfSelectedVirtualParents {
				knownPastOfSelectedVirtualParents.Add(current)
			}
		}
		if isInPastOfSelectedVirtualParents {
			log.Tracef("Skipping block %s because it's in the past of one (or more) of the selected virtual parents", current)
			continue
		}

		log.Tracef("Incrementing the merge set size increase")
		mergeSetIncrease++

		if (mergeSetSize + mergeSetIncrease) > csm.mergeSetSizeLimit {
			log.Debugf("The merge set would increase by more than the limit with block %s", candidate)
			return false, current, mergeSetIncrease, nil
		}

		parents, err := csm.dagTopologyManager.Parents(stagingArea, current)
		if err != nil {
			return false, nil, 0, err
		}
		for _, parent := range parents {
			if !visited.Contains(parent) {
				visited.Add(parent)
				queue = append(queue, parent)
			}
		}
	}
	log.Debugf("The resolved merge set size increase is: %d", mergeSetIncrease)

	return true, nil, mergeSetIncrease, nil
}

func (csm *consensusStateManager) boundedMergeBreakingParents(stagingArea *model.StagingArea,
	parents []*externalapi.DomainHash,
) ([]*externalapi.DomainHash, error) {
	onEnd := logger.LogAndMeasureExecutionTime(log, "boundedMergeBreakingParents")
	defer onEnd()

	log.Tracef("boundedMergeBreakingParents start for parents: %s", parents)

	log.Debug("Temporarily setting virtual to all parents, so that we can run ghostdag on it")
	err := csm.dagTopologyManager.SetParents(stagingArea, model.VirtualBlockHash, parents)
	if err != nil {
		return nil, err
	}

	err = csm.ghostdagManager.GHOSTDAG(stagingArea, model.VirtualBlockHash)
	if err != nil {
		return nil, err
	}

	virtualMergeDepthRoot, err := csm.mergeDepthManager.VirtualMergeDepthRoot(stagingArea)
	if err != nil {
		return nil, err
	}
	log.Debugf("The merge depth root of virtual is: %s", virtualMergeDepthRoot)

	potentiallyKosherizingBlocks, err := csm.mergeDepthManager.NonBoundedMergeDepthViolatingBlues(stagingArea, model.VirtualBlockHash, virtualMergeDepthRoot)
	if err != nil {
		return nil, err
	}
	log.Debugf("The potentially kosherizing blocks are: %s", potentiallyKosherizingBlocks)

	var badReds []*externalapi.DomainHash

	virtualGHOSTDAGData, err := csm.ghostdagDataStore.Get(csm.databaseContext, stagingArea, model.VirtualBlockHash, false)
	if database.IsNotFoundError(err) {
		log.Infof("boundedMergeBreakingParents failed to retrieve with %s\n", model.VirtualBlockHash)
		return nil, err
	}
	if err != nil {
		return nil, err
	}
	for _, redBlock := range virtualGHOSTDAGData.MergeSetReds() {
		log.Debugf("Check whether red block %s is kosherized", redBlock)
		isMergeDepthRootInPast, err := csm.dagTopologyManager.IsAncestorOf(stagingArea, virtualMergeDepthRoot, redBlock)
		if err != nil {
			return nil, err
		}
		if isMergeDepthRootInPast {
			log.Debugf("Skipping red block %s because it has the virtual's"+
				" merge depth root in its past", redBlock)
			continue
		}

		isKosherized := false
		for _, potentiallyKosherizingBlock := range potentiallyKosherizingBlocks {
			isKosherized, err = csm.dagTopologyManager.IsAncestorOf(stagingArea, redBlock, potentiallyKosherizingBlock)
			if err != nil {
				return nil, err
			}
			log.Debugf("Red block %s is an ancestor of potentially kosherizing "+
				"block %s, therefore the red block is kosher", redBlock, potentiallyKosherizingBlock)
			if isKosherized {
				break
			}
		}
		if !isKosherized {
			log.Debugf("Red block %s is not kosher. Adding it to the bad reds set", redBlock)
			badReds = append(badReds, redBlock)
		}
	}

	var boundedMergeBreakingParents []*externalapi.DomainHash
	for _, parent := range parents {
		log.Debugf("Checking whether parent %s breaks the bounded merge set", parent)
		isBadRedInPast := false
		for _, badRedBlock := range badReds {
			isBadRedInPast, err = csm.dagTopologyManager.IsAncestorOf(stagingArea, badRedBlock, parent)
			if err != nil {
				return nil, err
			}
			if isBadRedInPast {
				log.Debugf("Parent %s is a descendant of bad red %s", parent, badRedBlock)
				break
			}
		}
		if isBadRedInPast {
			log.Debugf("Adding parent %s to the bounded merge breaking parents set", parent)
			boundedMergeBreakingParents = append(boundedMergeBreakingParents, parent)
		}
	}

	return boundedMergeBreakingParents, nil
}
