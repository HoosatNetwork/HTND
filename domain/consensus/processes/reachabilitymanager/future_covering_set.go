package reachabilitymanager

import (
	"github.com/HoosatNetwork/HTND/domain/consensus/database"
	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/reachabilitydata"
)

// insertToFutureCoveringSet inserts the given block into this node's FutureCoveringSet
// while keeping it ordered by interval.
// If a block B ∈ node.FutureCoveringSet exists such that its interval
// contains block's interval, block need not be added. If block's
// interval contains B's interval, it replaces it.
//
// Notes:
//   - Intervals never intersect unless one contains the other
//     (this follows from the tree structure and the indexing rule).
//   - Since node.FutureCoveringSet is kept ordered, a binary search can be
//     used for insertion/queries.
//   - Although reindexing may change a block's interval, the
//     is-superset relation will by definition
//     be always preserved.
func (rt *reachabilityManager) insertToFutureCoveringSet(stagingArea *model.StagingArea, node, futureNode *externalapi.DomainHash) error {
	data, err := rt.reachabilityDataStore.ReachabilityData(rt.databaseContext, stagingArea, node)
	if err != nil {
		if !database.IsNotFoundError(err) {
			return err
		}
		data = reachabilitydata.EmptyReachabilityData()
	}
	futureCoveringSet := data.FutureCoveringSet()

	futureInterval, err := rt.interval(stagingArea, futureNode)
	if err != nil {
		return err
	}

	ancestorIndex, ok, err := rt.findAncestorIndexOfNodeByIntervalEnd(stagingArea, orderedTreeNodeSet(futureCoveringSet), futureInterval.End)
	if err != nil {
		return err
	}

	if !ok {
		// Insert at the beginning.
		newSet := make([]*externalapi.DomainHash, len(futureCoveringSet)+1)
		newSet[0] = futureNode
		copy(newSet[1:], futureCoveringSet)
		rt.stageData(stagingArea, node, reachabilitydata.New(data.Children(), data.Parent(), data.Interval(), model.FutureCoveringTreeNodeSet(newSet)))
		return nil
	}

	candidate := futureCoveringSet[ancestorIndex]
	candidateInterval, err := rt.interval(stagingArea, candidate)
	if err != nil {
		return err
	}

	if intervalContains(candidateInterval, futureInterval) {
		// candidate is an ancestor of futureNode, no need to insert
		return nil
	}
	if intervalContains(futureInterval, candidateInterval) {
		// futureNode is an ancestor of candidate, and can thus replace it
		newSet := futureCoveringSet.Clone()
		newSet[ancestorIndex] = futureNode
		rt.stageData(stagingArea, node, reachabilitydata.New(data.Children(), data.Parent(), data.Interval(), newSet))
		return nil
	}

	// Insert futureNode in the correct index to maintain futureCoveringTreeNodeSet as
	// a sorted-by-interval list.
	// Note that ancestorIndex might be equal to len(futureCoveringTreeNodeSet)
	insertIndex := ancestorIndex + 1
	newSet := make([]*externalapi.DomainHash, len(futureCoveringSet)+1)
	copy(newSet, futureCoveringSet[:insertIndex])
	newSet[insertIndex] = futureNode
	copy(newSet[insertIndex+1:], futureCoveringSet[insertIndex:])
	rt.stageData(stagingArea, node, reachabilitydata.New(data.Children(), data.Parent(), data.Interval(), model.FutureCoveringTreeNodeSet(newSet)))
	return nil
}

// futureCoveringSetHasAncestorOf resolves whether the given node `other` is in the subtree of
// any node in this.FutureCoveringSet.
// See insertNode method for the complementary insertion behavior.
//
// Like the insert method, this method also relies on the fact that
// this.FutureCoveringSet is kept ordered by interval to efficiently perform a
// binary search over this.FutureCoveringSet and answer the query in
// O(log(|futureCoveringTreeNodeSet|)).
func (rt *reachabilityManager) futureCoveringSetHasAncestorOf(stagingArea *model.StagingArea,
	this, other *externalapi.DomainHash,
) (bool, error) {
	futureCoveringSet, err := rt.futureCoveringSet(stagingArea, this)
	if err != nil {
		return false, err
	}

	ancestorIndex, ok, err := rt.findAncestorIndexOfNode(stagingArea, orderedTreeNodeSet(futureCoveringSet), other)
	if err != nil {
		return false, err
	}

	if !ok {
		// No candidate to contain other
		return false, nil
	}

	candidate := futureCoveringSet[ancestorIndex]

	return rt.IsReachabilityTreeAncestorOf(stagingArea, candidate, other)
}
