//go:build pruningtest

package pruningmanager

import (
	"context"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/database"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/virtual"
	"github.com/HoosatNetwork/HTND/v2/util/staging"
)

// RunLegacyHistoryDeletion deletes the history of the lowest recorded job the way the
// finish did before jobs existed (base c7d5c3f3e deletePastBlocks without pruneTips,
// which the finish already ran): the downward heap walk below, staged into one staging
// area and committed at once. It then removes the job record. Lab tests run it on a
// twin node from the "history-recorded" hook as the reference end state. The caller
// holds the consensus lock.
func RunLegacyHistoryDeletion(manager model.PruningManager) error {
	pm := manager.(*pruningManager)
	jobs, err := pm.pruningStore.(historyJobStore).HistoryDeletionJobs(pm.databaseContext)
	if err != nil || len(jobs) == 0 {
		return err
	}
	job := jobs[0]
	stagingArea := model.NewStagingArea()
	queue := pm.dagTraversalManager.NewDownHeap(stagingArea)
	if err = queue.PushSlice(job.PrunedTips); err != nil {
		return err
	}
	parents, err := pm.dagTopologyManager.Parents(stagingArea, job.DeletionPoint)
	if err != nil {
		return err
	}
	if !virtual.ContainsOnlyVirtualGenesis(parents) {
		if err = queue.PushSlice(parents); err != nil {
			return err
		}
	}
	blocksToKeep, err := pm.calculateBlocksToKeep(stagingArea, job.DeletionPoint)
	if err != nil {
		return err
	}
	if err = pm.legacyDeleteBlocksDownward(stagingArea, queue, blocksToKeep); err != nil {
		return err
	}
	if err = staging.CommitAllChanges(pm.databaseContext, stagingArea); err != nil {
		return err
	}
	dbTx, err := pm.databaseContext.BeginUnindexed()
	if err != nil {
		return err
	}
	defer dbTx.RollbackUnlessClosed()
	if err = pm.pruningStore.(historyJobStore).DeleteHistoryDeletionJobDirect(dbTx, job.Index); err != nil {
		return err
	}
	return dbTx.Commit()
}

// legacyDeleteBlocksDownward is base c7d5c3f3e deleteBlocksDownward, verbatim.
func (pm *pruningManager) legacyDeleteBlocksDownward(stagingArea *model.StagingArea,
	queue model.BlockHeap, blocksToKeep map[externalapi.DomainHash]struct{},
) error {
	visited := map[externalapi.DomainHash]struct{}{}
	// Prune everything in the queue including its past, unless it's in `blocksToKeep`
	for queue.Len() > 0 {
		current := queue.Pop()
		if _, ok := visited[*current]; ok {
			continue
		}
		visited[*current] = struct{}{}

		shouldAddParents := true
		if _, ok := blocksToKeep[*current]; !ok {
			alreadyPruned, err := pm.deleteBlock(stagingArea, current)
			if err != nil {
				return err
			}
			shouldAddParents = !alreadyPruned
		}

		if shouldAddParents {
			parents, err := pm.dagTopologyManager.Parents(stagingArea, current)
			if err != nil {
				return err
			}

			if !virtual.ContainsOnlyVirtualGenesis(parents) {
				err = queue.PushSlice(parents)
				if err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// HistoryDeletionPlan returns the plan of the lowest recorded job and that job's index; nil
// and false without a job. With an owner it plans on a snapshot, as the owner does: what is
// stored, not what the mask shows. Without one, on the live stores. The caller holds the lock.
func HistoryDeletionPlan(manager model.PruningManager) (plan []*externalapi.DomainHash, index uint64, found bool,
	err error) {
	pm := manager.(*pruningManager)
	jobs, err := pm.pruningStore.(historyJobStore).HistoryDeletionJobs(pm.databaseContext)
	if err != nil || len(jobs) == 0 {
		return nil, 0, false, err
	}
	if pm.historyMask == nil {
		plan, err = pm.planHistoryDeletion(model.NewStagingArea(), jobs[0])
		return plan, jobs[0].Index, true, err
	}
	snapshot, err := pm.databaseContext.(interface {
		NewSnapshot() (*database.ReadSnapshot, error)
	}).NewSnapshot()
	if err != nil {
		return nil, 0, false, err
	}
	defer func() {
		if closeErr := snapshot.Close(); err == nil {
			err = closeErr
		}
	}()
	view, err := pm.historyReadView(context.Background(), snapshot)
	if err != nil {
		return nil, 0, false, err
	}
	plan, err = view.planHistoryDeletion(model.NewStagingArea(), jobs[0])
	return plan, jobs[0].Index, true, err
}

// HistoryMaskBlocks returns the blocks the published history mask masks, sorted; nil
// without a published mask. withData reports whether it masks their data too.
func HistoryMaskBlocks(manager model.PruningManager) (blocks []*externalapi.DomainHash, withData bool) {
	pm := manager.(*pruningManager)
	if pm.historyMask == nil {
		return nil, false
	}
	return pm.historyMask.BlocksForTest()
}
