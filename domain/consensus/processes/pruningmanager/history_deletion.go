package pruningmanager

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/database"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/datastructures/blockheaderstore"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/datastructures/blockrelationstore"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/datastructures/blockstatusstore"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/datastructures/blockwindowheapslicestore"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/datastructures/consensusstatestore"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/datastructures/daablocksstore"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/datastructures/daawindowstore"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/datastructures/ghostdagdatastore"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/datastructures/pruningstore"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/datastructures/reachabilitydatastore"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/processes/dagtopologymanager"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/processes/dagtraversalmanager"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/processes/ghostdagmanager"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/processes/reachabilitymanager"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/virtual"
)

// History below a deletion point D is deleted by a durable job . The finish of a
// pruning point update records {index, D, prunedTips} in the commit that clears the
// pending flag, so history is deleted only after the flag is cleared. The job then
// plans the blocks to delete (today's keep set and walk), and deletes them in batches
// of HistoryDeletionBatchBlocks, parents before children; its last batch also deletes
// its record. Deleting bottom-up keeps the header-only region past-closed after every
// batch, so after a restart a new walk of D finds exactly the remainder: no cursor is
// persisted. Jobs run one at a time, lowest index first.
//
// With an owner (deferred updates on a backend with read snapshots) a job runs on the
// owner's goroutine: the keep set and the walk run on a snapshot without the consensus
// lock. One short hold then publishes the whole plan as a logical delete (a
// database.HistoryMask the six stores consult in every reader, locked or lock-free):
// from there on every reader sees the state base reached with its single locked deletion
// commit. The batches commit without the lock, behind the mask; each is followed by an
// eviction of what it deleted from the store caches, also without the lock (the caches it
// touches are concurrency-safe, and while the mask is published no reader can add a masked
// block to them). After the last eviction one more short hold removes the mask. No
// database or per-block work happens under the lock. A failed batch leaves
// the mask published, so no reader sees a planned block come back; the retry re-plans
// and republishes the remainder before its first commit. A crash loses the mask, and
// what the database then holds is the state between two batches, past-closed, which a
// resumed job republishes and finishes. Without an owner a job runs right after the
// finish, under the same lock hold, as the deletion did before it became a job.

// historyDeletionStore is implemented by the five stores a deleted block loses data in.
type historyDeletionStore interface {
	DeleteBlockDataDirect(dbTx model.DBWriter, blockHash *externalapi.DomainHash) error
	EvictDeleted(blockHashes []*externalapi.DomainHash)
	SetHistoryMask(mask *database.HistoryMask)
}

type historyStatusStore interface {
	StageStatusDirect(dbTx model.DBWriter, blockHash *externalapi.DomainHash, blockStatus externalapi.BlockStatus) error
	EvictDeleted(blockHashes []*externalapi.DomainHash)
	SetHistoryMask(mask *database.HistoryMask)
}

type historyCountStore interface {
	PutDeletionCountsDirect(dbTx model.DBWriter, deletedBlocks uint64) error
	RewriteStaleLegacyCountDirect(dbTx model.DBWriter) (bool, error)
	NormalizeCountsForDowngradeDirect(dbTx model.DBWriter) error
}

type historyJobStore interface {
	StageHistoryDeletionJob(stagingArea *model.StagingArea, job *pruningstore.HistoryDeletionJob)
	HistoryDeletionJobs(dbContext model.DBReader) ([]*pruningstore.HistoryDeletionJob, error)
	DeleteHistoryDeletionJobDirect(dbTx model.DBWriter, index uint64) error
}

// ConfigureHistoryDeletionOwner installs request as the way to hand history deletion
// jobs to a background owner, if the backend opens the read snapshots the owner plans
// on. It reports whether it did. Call before any update can finish.
func ConfigureHistoryDeletionOwner(manager model.PruningManager, prefix model.DBBucket, request func()) (bool, error) {
	pm := manager.(*pruningManager)
	opener, ok := pm.databaseContext.(interface {
		NewSnapshot() (*database.ReadSnapshot, error)
	})
	if !ok {
		return false, nil
	}
	probe, err := opener.NewSnapshot()
	if errors.Is(err, database.ErrSnapshotUnsupported) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err = probe.Close(); err != nil {
		return false, err
	}
	pm.historyPrefix, pm.requestHistoryDeletion = prefix, request
	pm.historyMask = &database.HistoryMask{}
	pm.blockStatusStore.(historyStatusStore).SetHistoryMask(pm.historyMask)
	for _, store := range pm.historyDataStores() {
		store.SetHistoryMask(pm.historyMask)
	}
	return true, nil
}

// ResumeHistoryDeletion rewrites a legacy blocks-count a crash left behind deletions, then
// hands the jobs a previous run left to the owner, or runs them now without one. Called at
// startup, where nothing else holds the consensus lock. Only the previous release reads
// the legacy count, so failing to rewrite it is logged and does not stop the start.
func ResumeHistoryDeletion(manager model.PruningManager) {
	pm := manager.(*pruningManager)
	if counts, ok := pm.blocksStore.(historyCountStore); ok {
		if err := pm.commitDirect(counts.RewriteStaleLegacyCountDirect); err != nil {
			log.Warnf("Rewriting the legacy blocks-count failed; the next block commit rewrites it: %s", err)
		}
	}
	pm.scheduleHistoryDeletion()
}

// NormalizeBlockCountsForDowngrade writes the exact legacy blocks-count and removes the
// split counters, which the previous release does not keep. The caller holds the consensus
// lock and has run every history deletion job.
func NormalizeBlockCountsForDowngrade(manager model.PruningManager) error {
	pm := manager.(*pruningManager)
	counts, ok := pm.blocksStore.(historyCountStore)
	if !ok {
		return nil
	}
	return pm.commitDirect(func(dbTx model.DBWriter) (bool, error) {
		return true, counts.NormalizeCountsForDowngradeDirect(dbTx)
	})
}

// commitDirect commits what write puts into a private transaction, if it reports writing.
func (pm *pruningManager) commitDirect(write func(dbTx model.DBWriter) (bool, error)) error {
	dbTx, err := pm.databaseContext.BeginUnindexed()
	if err != nil {
		return err
	}
	defer dbTx.RollbackUnlessClosed()
	wrote, err := write(dbTx)
	if err != nil || !wrote {
		return err
	}
	return dbTx.Commit()
}

// RunHistoryDeletionJobs runs the recorded jobs until none is left. lock is the
// consensus lock, taken per batch; nil means the caller holds it for the whole run.
func RunHistoryDeletionJobs(manager model.PruningManager, ctx context.Context, lock sync.Locker) error {
	return manager.(*pruningManager).runHistoryDeletionJobs(ctx, lock)
}

// recordHistoryDeletion stages, into the finish's commit, the pruned tips and the job
// that deletes the history below deletionPoint.
func (pm *pruningManager) recordHistoryDeletion(stagingArea *model.StagingArea, deletionPoint *externalapi.DomainHash) error {
	virtualParents, err := pm.dagTopologyManager.Parents(stagingArea, model.VirtualBlockHash)
	if err != nil {
		return err
	}
	prunedTips, err := pm.pruneTips(stagingArea, deletionPoint, virtualParents)
	if err != nil {
		return err
	}
	index, err := pm.pruningStore.CurrentPruningPointIndex(pm.databaseContext, stagingArea)
	if err != nil {
		return err
	}
	pm.pruningStore.(historyJobStore).StageHistoryDeletionJob(stagingArea,
		&pruningstore.HistoryDeletionJob{Index: index, DeletionPoint: deletionPoint, PrunedTips: prunedTips})
	return nil
}

// scheduleHistoryDeletion runs after a commit that recorded a job. Without an owner the
// caller holds the consensus lock, and the job runs here. A failure keeps the record
// and every finished batch; the next finish or restart resumes it.
func (pm *pruningManager) scheduleHistoryDeletion() {
	pm.derivationHook("history-recorded")
	if pm.requestHistoryDeletion != nil {
		pm.requestHistoryDeletion()
		return
	}
	if err := pm.runHistoryDeletionJobs(context.Background(), nil); err != nil {
		log.Errorf("History deletion below the pruning point failed; its job keeps its progress and resumes "+
			"at the next pruning point move or restart: %s", err)
	}
}

func (pm *pruningManager) runHistoryDeletionJobs(ctx context.Context, lock sync.Locker) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		jobs, err := pm.pruningStore.(historyJobStore).HistoryDeletionJobs(pm.databaseContext)
		if err != nil || len(jobs) == 0 {
			return err
		}
		if err = pm.runHistoryDeletionJob(ctx, lock, jobs[0]); err != nil {
			return err
		}
	}
}

func (pm *pruningManager) runHistoryDeletionJob(ctx context.Context, lock sync.Locker,
	job *pruningstore.HistoryDeletionJob) error {
	if lock != nil {
		lock = testJournalLock(pm.diagnosticLock(lock, "history"))
	}
	var plan []*externalapi.DomainHash
	var err error
	if lock == nil {
		// The caller holds the lock for the whole run. The live stores must show what is
		// stored, not the mask a failed run of the owner may have left.
		pm.historyMask.Publish(nil)
		plan, err = pm.planHistoryDeletion(model.NewStagingArea(), job)
	} else {
		plan, err = pm.planHistoryDeletionOnSnapshot(ctx, lock, job)
	}
	if err != nil {
		return err
	}
	pm.derivationHook("history-planned")
	if pm.historyMask != nil {
		// Before the first batch commits: from this hold on, every reader sees the whole
		// plan deleted. The set is built off-lock; publishing it is one pointer store.
		set := database.NewHistoryMaskSet(plan, !pm.isArchivalNode)
		testLockPhase(lock, "deletion-publish")
		pm.historyHold(lock, func() { pm.historyMask.Publish(set) })
		pm.derivationHook("history-mask-published")
	}
	// An empty plan still runs one batch: it deletes the record.
	for start := 0; ; start += HistoryDeletionBatchBlocks {
		if err := ctx.Err(); err != nil {
			return err
		}
		end := min(start+HistoryDeletionBatchBlocks, len(plan))
		if err := pm.deleteHistoryBatch(lock, job, plan[start:end], end == len(plan)); err != nil {
			return err
		}
		pm.derivationHook("history-batch")
		if end == len(plan) {
			log.Infof("History below %s deleted (job %d: %d blocks)", job.DeletionPoint, job.Index, len(plan))
			return nil
		}
	}
}

// planHistoryDeletion returns the blocks the job deletes, in ascending (blue work,
// hash) order: exactly the set today's downward walk deleted. Roots are the pruned
// tips and the parents of D; a kept block is passed through; a header-only block
// stops the descent. The set depends on each block's own status and keep membership
// only, never on the visiting order. Blue work strictly increases from parent to
// child, so the order deletes parents before children.
func (pm *pruningManager) planHistoryDeletion(stagingArea *model.StagingArea,
	job *pruningstore.HistoryDeletionJob) ([]*externalapi.DomainHash, error) {
	blocksToKeep, err := pm.calculateBlocksToKeep(stagingArea, job.DeletionPoint)
	if err != nil {
		return nil, err
	}

	queue := append([]*externalapi.DomainHash(nil), job.PrunedTips...)
	parents, err := pm.dagTopologyManager.Parents(stagingArea, job.DeletionPoint)
	if err != nil {
		return nil, err
	}
	if !virtual.ContainsOnlyVirtualGenesis(parents) {
		queue = append(queue, parents...)
	}
	var plan []*externalapi.BlockGHOSTDAGDataHashPair
	visited := map[externalapi.DomainHash]struct{}{}
	for len(queue) > 0 {
		current := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		if _, ok := visited[*current]; ok {
			continue
		}
		visited[*current] = struct{}{}
		// Every visited block has GHOSTDAG data, as the downward walk's heap required.
		ghostdagData, err := pm.ghostdagDataStore.Get(pm.databaseContext, stagingArea, current, false)
		if err != nil {
			return nil, err
		}
		if _, keep := blocksToKeep[*current]; !keep {
			status, err := pm.blockStatusStore.Get(pm.databaseContext, stagingArea, current)
			switch {
			case database.IsNotFoundError(err):
				log.Infof("deleteBlock failed to retrieve with %s\n", current)
			case err != nil:
				return nil, err
			case status == externalapi.StatusHeaderOnly:
				continue
			default:
				plan = append(plan, &externalapi.BlockGHOSTDAGDataHashPair{Hash: current, GHOSTDAGData: ghostdagData})
			}
		}
		parents, err := pm.dagTopologyManager.Parents(stagingArea, current)
		if err != nil {
			return nil, err
		}
		if !virtual.ContainsOnlyVirtualGenesis(parents) {
			queue = append(queue, parents...)
		}
	}
	slices.SortFunc(plan, func(a, b *externalapi.BlockGHOSTDAGDataHashPair) int {
		if c := a.GHOSTDAGData.BlueWork().Cmp(b.GHOSTDAGData.BlueWork()); c != 0 {
			return c
		}
		if a.Hash.Less(b.Hash) {
			return -1
		}
		return 1
	})
	hashes := make([]*externalapi.DomainHash, len(plan))
	for i, pair := range plan {
		hashes[i] = pair.Hash
	}
	return hashes, nil
}

// planHistoryDeletionOnSnapshot holds the lock only to name the job's pruning point and
// open a read snapshot, then plans on private stores over the snapshot. Reachability
// is read consistently there, and no live cache is touched off-lock. Once ctx is done
// every read of the snapshot fails with ctx's error, so a stop does not wait for the keep
// set (its anticone walks the whole future of the pruning point) or the walk.
func (pm *pruningManager) planHistoryDeletionOnSnapshot(ctx context.Context, lock sync.Locker,
	job *pruningstore.HistoryDeletionJob) (plan []*externalapi.DomainHash, err error) {
	if pm.pruningDiagnostics {
		started := time.Now()
		defer func() {
			log.Infof("Pruning diagnostics: phase=planHistoryDeletionOnSnapshot elapsed=%s", time.Since(started))
		}()
	}
	testLockPhase(lock, "deletion-snapshot")
	lock.Lock()
	testHistoryTarget(pm, lock, job.Index)
	var snapshot *database.ReadSnapshot
	_, err = pm.pruningStore.PruningPointByIndex(pm.databaseContext, model.NewStagingArea(), job.Index)
	if err == nil {
		snapshot, err = pm.databaseContext.(interface {
			NewSnapshot() (*database.ReadSnapshot, error)
		}).NewSnapshot()
	}
	lock.Unlock()
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := snapshot.Close(); err == nil {
			err = closeErr
		}
	}()
	view, err := pm.historyReadView(ctx, snapshot)
	if err != nil {
		return nil, err
	}
	return view.planHistoryDeletion(model.NewStagingArea(), job)
}

// cancelableSnapshot fails every read with ctx's error once ctx is done.
type cancelableSnapshot struct {
	*database.ReadSnapshot
	ctx context.Context
}

func (s cancelableSnapshot) Get(key model.DBKey) ([]byte, error) {
	if err := s.ctx.Err(); err != nil {
		return nil, err
	}
	return s.ReadSnapshot.Get(key)
}

func (s cancelableSnapshot) Has(key model.DBKey) (bool, error) {
	if err := s.ctx.Err(); err != nil {
		return false, err
	}
	return s.ReadSnapshot.Has(key)
}

func (s cancelableSnapshot) Cursor(bucket model.DBBucket) (model.DBCursor, error) {
	if err := s.ctx.Err(); err != nil {
		return nil, err
	}
	cursor, err := s.ReadSnapshot.Cursor(bucket)
	if err != nil {
		return nil, err
	}
	return &cancelableHistoryCursor{DBCursor: cursor, ctx: s.ctx}, nil
}

// historyReadView returns a manager with private stores over snapshot, read until ctx is
// done: what calculateBlocksToKeep and the walk read, built as the factory builds them.
func (pm *pruningManager) historyReadView(ctx context.Context, readSnapshot *database.ReadSnapshot) (*pruningManager, error) {
	snapshot := cancelableSnapshot{readSnapshot, ctx}
	p := database.MakeBucket(bytes.Clone(pm.historyPrefix.Path()))
	level := p.Bucket([]byte{0})
	header, err := blockheaderstore.New(snapshot, p, 128, false)
	if err != nil {
		return nil, err
	}
	ghost := ghostdagdatastore.New(level, 128, false)
	reach := reachabilitydatastore.New(level, 128, false)
	old, err := reach.HasReachabilityData(snapshot, model.NewStagingArea(), model.VirtualGenesisBlockHash)
	if err != nil {
		return nil, err
	}
	if !old {
		reach = reachabilitydatastore.New(p, 128, false)
	}
	reachability := reachabilitymanager.New(snapshot, ghost, reach)
	topology := dagtopologymanager.New(snapshot, reachability, blockrelationstore.New(level, 128, false), ghost)
	daaBlocks := daablocksstore.New(p, 128, 128, false)
	genesis := *pm.genesisHash
	ghostdag := ghostdagmanager.New(snapshot, topology, nil, ghost, header, consensusstatestore.New(p, 128, false),
		pm.k, &genesis, daaBlocks, pm.powScores)
	traversal := dagtraversalmanager.New(snapshot, topology, ghost, reachability, ghostdag,
		daawindowstore.New(p, 128, false), blockwindowheapslicestore.New(128, false), &genesis,
		pm.difficultyAdjustmentWindowSize, daaBlocks, pm.powScores)
	ghostdag.SetDAGTraversalManager(traversal)
	return &pruningManager{databaseContext: snapshot, genesisHash: &genesis, powScores: pm.powScores,
		difficultyAdjustmentWindowSize: pm.difficultyAdjustmentWindowSize,
		ghostdagDataStore:              ghost, blockStatusStore: blockstatusstore.New(p, 128, false),
		daaBlocksStore: daaBlocks, dagTopologyManager: topology, dagTraversalManager: traversal}, nil
}

func (pm *pruningManager) historyDataStores() []historyDeletionStore {
	return []historyDeletionStore{pm.multiSetStore.(historyDeletionStore), pm.acceptanceDataStore.(historyDeletionStore),
		pm.blocksStore.(historyDeletionStore), pm.utxoDiffStore.(historyDeletionStore), pm.daaBlocksStore.(historyDeletionStore)}
}

// historyHold runs f under the consensus lock; with a nil lock
// lock the caller already holds it.
func (pm *pruningManager) historyHold(lock sync.Locker, f func()) {
	if lock != nil {
		lock.Lock()
		defer lock.Unlock()
	}
	f()
}

// deleteHistoryBatch writes exactly the keys today's deleteBlock stages for each block
// of batch (archival: the status only), and blocks-deleted, into a private transaction
// and commits it, all without the lock: the published mask already shows these blocks
// deleted. It then evicts the deleted entries from the caches, also without the lock, so
// that none is left once the mask is removed; after the last batch's eviction one hold
// removes the mask. The last batch of a job also deletes the job's record.
func (pm *pruningManager) deleteHistoryBatch(lock sync.Locker, job *pruningstore.HistoryDeletionJob,
	batch []*externalapi.DomainHash, last bool) error {
	if pm.pruningDiagnostics {
		started := time.Now()
		defer func() { log.Infof("Pruning diagnostics: phase=deleteHistoryBatch elapsed=%s", time.Since(started)) }()
	}
	dbTx, err := pm.databaseContext.BeginUnindexed()
	if err != nil {
		return err
	}
	defer dbTx.RollbackUnlessClosed()
	statuses := pm.blockStatusStore.(historyStatusStore)
	stores := pm.historyDataStores()
	for _, blockHash := range batch {
		if err = statuses.StageStatusDirect(dbTx, blockHash, externalapi.StatusHeaderOnly); err != nil {
			return err
		}
		if pm.isArchivalNode {
			continue
		}
		for _, store := range stores {
			if err = store.DeleteBlockDataDirect(dbTx, blockHash); err != nil {
				return err
			}
		}
	}
	if !pm.isArchivalNode {
		err = pm.blocksStore.(historyCountStore).PutDeletionCountsDirect(dbTx, uint64(len(batch)))
		if err != nil {
			return err
		}
	}
	if last {
		if err = pm.pruningStore.(historyJobStore).DeleteHistoryDeletionJobDirect(dbTx, job.Index); err != nil {
			return err
		}
	}
	// A failed commit leaves the mask published: the retry republishes the remainder.
	pm.derivationHook("history-before-batch-commit")
	if err = dbTx.Commit(); err != nil {
		return err
	}
	pm.derivationHook("history-batch-commit")
	if last {
		pm.derivationHook("history-last-batch-commit")
	}
	// Off-lock: while the mask is published no reader adds a masked block to a cache, so the
	// eviction only has to finish before the mask is removed below.
	statuses.EvictDeleted(batch)
	if !pm.isArchivalNode {
		for _, store := range stores {
			store.EvictDeleted(batch)
		}
	}
	if last && pm.historyMask != nil {
		testLockPhase(lock, "deletion-retire")
		pm.historyHold(lock, func() { pm.historyMask.Publish(nil) })
		testMaskRemoved(job.Index, pm.historyMask)
		pm.derivationHook("history-mask-removed")
	}
	return nil
}

// ConfigureHistoryJobs enables new jobs for the deferred owner and resumes durable work.
func ConfigureHistoryJobs(manager model.PruningManager) (bool, error) {
	pm := manager.(*pruningManager)
	jobs, err := pm.pruningStore.(historyJobStore).HistoryDeletionJobs(pm.databaseContext)
	if err != nil {
		return false, err
	}
	pm.historyJobsEnabled = (pm.journalRequested && pm.requestUTXOSetScan != nil) || pm.journal != nil || len(jobs) > 0
	return pm.historyJobsEnabled, nil
}

// Cursor cancellation must also cover an iterator opened before shutdown, rather
// than only the call that opened it. Close propagates cancellation if Next stopped.
type cancelableHistoryCursor struct {
	model.DBCursor
	ctx context.Context
}

func (c *cancelableHistoryCursor) First() bool { return c.ctx.Err() == nil && c.DBCursor.First() }
func (c *cancelableHistoryCursor) Next() bool  { return c.ctx.Err() == nil && c.DBCursor.Next() }
func (c *cancelableHistoryCursor) Seek(key model.DBKey) error {
	if err := c.ctx.Err(); err != nil {
		return err
	}
	return c.DBCursor.Seek(key)
}
func (c *cancelableHistoryCursor) Key() (model.DBKey, error) {
	if err := c.ctx.Err(); err != nil {
		return nil, err
	}
	return c.DBCursor.Key()
}
func (c *cancelableHistoryCursor) Value() ([]byte, error) {
	if err := c.ctx.Err(); err != nil {
		return nil, err
	}
	return c.DBCursor.Value()
}
func (c *cancelableHistoryCursor) Close() error { return errors.Join(c.DBCursor.Close(), c.ctx.Err()) }
