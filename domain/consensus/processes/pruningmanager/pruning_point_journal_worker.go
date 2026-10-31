package pruningmanager

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/database"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/datastructures/pruningstore"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
)

// ConfigureJournalWorker runs before any background goroutine. The on-disk marker
// requires recovery on restart. Unsupported backends
// fail before migration. Legacy pending data is recovered with the old recorded method.
func ConfigureJournalWorker(manager model.PruningManager, prefix model.DBBucket, hook func(string),
	policy pruningstore.JournalDiskPolicy) error {
	pm := manager.(*pruningManager)
	pm.setDerivationHook(hook)
	pm.journalPolicy = policy
	j := pruningstore.NewJournal(pm.databaseContext, prefix, pm.pruningStore)
	j.Policy = policy.WithDefaults()
	j.Archival = pm.isArchivalNode
	j.SetTestHook(hook)
	state, e := j.Load()
	if e != nil {
		return e
	}
	pm.journalPrefix, pm.journalRequested = prefix, true
	if pm.requestUTXOSetScan == nil && state == nil {
		return nil
	}
	if pm.requestUTXOSetScan == nil {
		return fmt.Errorf("pruning journal: requires deferred pruning updates")
	}
	opener, ok := pm.databaseContext.(interface {
		NewSnapshot() (*database.ReadSnapshot, error)
	})
	if !ok {
		if state == nil {
			pm.journalRequested = false
			return nil
		}
		return fmt.Errorf("pruning journal: snapshot backend unavailable with durable work")
	}
	probe, e := opener.NewSnapshot()
	if errors.Is(e, database.ErrSnapshotUnsupported) && state == nil {
		// A supported synchronous backend remains usable without enabling the journal.
		pm.journalRequested = false
		return nil
	}
	if e != nil {
		return e
	}
	if e = probe.Close(); e != nil {
		return e
	}
	// Snapshot visibility alone does not establish durable journal admission.
	admission, ok := pm.databaseContext.(interface{ CheckPruningJournalAdmission() error })
	if !ok {
		return fmt.Errorf("pruning journal: durable journal admission unavailable")
	}
	if err := admission.CheckPruningJournalAdmission(); err != nil {
		return err
	}
	// Declared disk policy, checked before any journal or legacy-recovery write.
	// Without in-flight work a refusal keeps (or normalizes back to) the synchronous
	// path. In-flight work stays installed: every write it still needs re-checks the
	// policy, so it waits resumably, retried on the next request, instead of failing.
	idle, err := pm.pruningStore.HadStartedUpdatingPruningPointUTXOSet(pm.databaseContext)
	if err != nil {
		return err
	}
	idle = !idle
	estimate := uint64(0)
	if state != nil {
		estimate = state.Remaining()
		if state.Phase == "complete" {
			estimate = state.Bytes
		}
	}
	check := j.CheckReserve(estimate)
	if state != nil && state.Phase == "ready" {
		check = j.CheckSealedReserve(estimate)
	}
	if err := check; err != nil {
		if !errors.Is(err, pruningstore.ErrJournalDiskReserve) {
			return err
		}
		switch {
		case state == nil:
			pm.journalRequested = false
			log.Warnf("Pruning point UTXO journal not enabled; the synchronous update path is kept: %s", err)
			return nil
		case state.Phase == "complete" && idle:
			if err := j.RemoveAfterFinish(context.Background()); err != nil {
				return err
			}
			pm.journalRequested = false
			log.Warnf("Pruning point UTXO journal normalized back to the synchronous update path: %s", err)
			return nil
		}
		log.Warnf("Pruning point UTXO journal job for index %d waits for disk space: %s", state.TargetIndex, err)
	}
	sealed := fmt.Sprintf("%d bytes once a job is sealed", j.Policy.SealedFloor)
	if j.Archival {
		sealed = "also once a job is sealed (archival: history deletion frees no space)"
	}
	log.Infof("Pruning point UTXO journal enabled; free space required before each journal write: %d bytes + %dx the estimated remaining job bytes, %s",
		j.Policy.Floor, j.Policy.Margin, sealed)
	// No marker means legacy semantics, including partially written legacy updates.
	if state == nil {
		pending, err := pm.pruningStore.HadStartedUpdatingPruningPointUTXOSet(pm.databaseContext)
		if err != nil {
			return err
		}
		if pending {
			if err = pm.updatePruningPoint(); err != nil {
				return err
			}
		}
		index, err := pm.pruningStore.CurrentPruningPointIndex(pm.databaseContext, model.NewStagingArea())
		if database.IsNotFoundError(err) {
			return nil
		}
		if err != nil {
			return err
		}
		target, err := pm.pruningStore.PruningPointByIndex(pm.databaseContext, model.NewStagingArea(), index)
		if err != nil {
			return err
		}
		if err = j.Initialize(index, target); err != nil {
			return err
		}
	}
	// A previous release run without --prepare-downgrade finished at least one move
	// after this manifest's target and was stopped inside the next one. Its finish
	// deleted the history the journal would replay from the manifest, so the job could
	// never derive and would be retried after every block. The live set is that
	// binary's: complete at the previous pruning point, the current move's diff maybe
	// part written under its recorded method. Finish that move as it would, as for an
	// unmarked database above; the check below then re-initialises at the new index.
	if state != nil && !idle {
		foreign, err := pm.historyDeletedAboveJournal(state)
		if err != nil {
			return err
		}
		if foreign {
			log.Infof("Pruning point UTXO journal manifest (index %d) predates moves another binary finished; "+
				"its pending move is finished on the synchronous path", state.TargetIndex)
			if err = pm.updatePruningPoint(); err != nil {
				return err
			}
		}
	}
	// A valid phase transition never clears pending before COMPLETE. Reject a
	// contradictory manifest before any consumer can serve a partially written set.
	state, e = j.Load()
	if e != nil {
		return e
	}
	pending, e := pm.pruningStore.HadStartedUpdatingPruningPointUTXOSet(pm.databaseContext)
	if e != nil {
		return e
	}
	if !pending {
		index, e := pm.pruningStore.CurrentPruningPointIndex(pm.databaseContext, model.NewStagingArea())
		if e != nil {
			return e
		}
		target, e := pm.pruningStore.PruningPointByIndex(pm.databaseContext, model.NewStagingArea(), index)
		if e != nil {
			return e
		}
		if state.Phase != "complete" || state.TargetIndex != index || state.Target != target.String() {
			// Another binary finished the legacy update after this manifest was written:
			// a previous release run without --prepare-downgrade. Its finished set is the
			// served set, so take it as a fresh Initialize does. The next move's full #41
			// scan checks the set against its commitment, as for any first job.
			log.Infof("Pruning point UTXO journal re-initialized at index %d (%s): its manifest (phase %s, index %d) "+
				"predates a pruning point UTXO set another binary finished", index, target, state.Phase, state.TargetIndex)
			if e = j.Initialize(index, target); e != nil {
				if !errors.Is(e, pruningstore.ErrJournalDiskReserve) {
					return e
				}
				// Not installed: the synchronous path serves this run; the next start retries.
				log.Warnf("Pruning point UTXO journal not re-initialized; the synchronous update path is kept: %s", e)
				return nil
			}
		}
	}
	pm.journal, pm.journalPrefix = j, prefix
	return nil
}

// historyDeletedAboveJournal reports whether a finish deleted history above the
// manifest's COMPLETE target: the selected parent of the next pruning point, which
// lies between the two, is header-only. This node never deletes above its COMPLETE
// target (each finish deletes below the target it checked, and a backlog finishes
// nothing), so only another binary's finish does. The index gap alone is no such
// sign: an own backlog has the same gap with its history intact, and its journal
// replays it. With that history kept, as with a deletion depth, the replay works
// for another binary's moves too: outpoints are never recreated.
func (pm *pruningManager) historyDeletedAboveJournal(state *pruningstore.JournalState) (bool, error) {
	stagingArea := model.NewStagingArea()
	index, err := pm.pruningStore.CurrentPruningPointIndex(pm.databaseContext, stagingArea)
	if err != nil {
		return false, err
	}
	if state.Phase != "complete" || state.TargetIndex+1 >= index {
		return false, nil
	}
	next, err := pm.pruningStore.PruningPointByIndex(pm.databaseContext, stagingArea, state.TargetIndex+1)
	if err != nil {
		return false, err
	}
	ghostdagData, err := pm.ghostdagDataStore.Get(pm.databaseContext, stagingArea, next, false)
	if err != nil {
		return false, err
	}
	status, err := pm.blockStatusStore.Get(pm.databaseContext, stagingArea, ghostdagData.SelectedParent())
	if err != nil {
		return false, err
	}
	return status == externalapi.StatusHeaderOnly, nil
}

// JournalActive lets exclusive import callers refuse takeover while the writer or
// recovery owns the live bucket. A pending flag remains true through every phase.
func JournalActive(manager model.PruningManager) bool {
	return manager.(*pruningManager).journal != nil
}

// ResetJournalAfterImport is called under the consensus lock only after a successful,
// exclusive import. It prevents old materialized indices being reused after adoption.
func ResetJournalAfterImport(manager model.PruningManager) error {
	pm := manager.(*pruningManager)
	if pm.journal == nil {
		if pm.journalRequested {
			return ConfigureJournalWorker(manager, pm.journalPrefix, pm.derivationTestHook(), pm.journalPolicy)
		}
		return nil
	}
	i, e := pm.pruningStore.CurrentPruningPointIndex(pm.databaseContext, model.NewStagingArea())
	if e != nil {
		return e
	}
	h, e := pm.pruningStore.PruningPointByIndex(pm.databaseContext, model.NewStagingArea(), i)
	if e != nil {
		return e
	}
	pm.appliedUTXOSetDiff = nil
	return pm.journal.Initialize(i, h)
}

// RunJournalWorker is invoked by the existing single #41 goroutine, never a goroutine
// per move. Selection can advance while it runs; history is retained until the current
// target's full scan passes and the existing atomic deletion/flag-clear commit succeeds.
func (pm *pruningManager) RunJournalWorker(ctx context.Context, lock sync.Locker) error {
	if pm.journal == nil {
		return nil
	}
	lock = pm.diagnosticLock(lock, "journal")
	lock = testJournalLock(lock)
	for {
		testLockPhase(lock, "capture")
		if e := ctx.Err(); e != nil {
			return e
		}
		lock.Lock()
		state, snapshot, target, begin, err := pm.captureJournalJob()
		if state != nil && target != nil {
			testLockTarget(lock, target)
		} else {
			testLockPhase(lock, "idle")
		}
		lock.Unlock()
		if err != nil || state == nil {
			return err
		}
		start := time.Now()
		// PREPARING is saved (synced) here, off-lock. A crash before it leaves no job, and
		// the next capture begins the same target from the same untouched COMPLETE base.
		if begin {
			next, err := pm.journal.Begin(state, state.TargetIndex+1, target)
			if err != nil {
				if e := snapshot.Close(); e != nil || !errors.Is(err, pruningstore.ErrJournalDiskReserve) {
					return errors.Join(err, e)
				}
				testLockPhase(lock, "fallback")
				lock.Lock()
				next, target, err = pm.journalDiskFallback(err)
				lock.Unlock()
				if err != nil {
					return err
				}
				snapshot = nil
			}
			state = next
		}
		if snapshot != nil {
			if e := pm.buildJournal(ctx, state, snapshot, target); e != nil {
				return e
			}
		}
		if state.Phase == "ready" {
			var applying time.Time
			if pm.pruningDiagnostics {
				applying = time.Now()
			}
			err := pm.journal.Apply(ctx, state)
			if pm.pruningDiagnostics {
				log.Infof("Pruning diagnostics: phase=journal-apply target=%s index=%d elapsed=%s", target, state.TargetIndex, time.Since(applying))
			}
			if err != nil {
				return err
			}
		}
		testLockPhase(lock, "admit")
		lock.Lock()
		// Only COMPLETE admits a scanner. This assignment and phase check are under the
		// same lock as scan admission; generation alone never authorizes partial data.
		pm.appliedUTXOSetDiff = &appliedUTXOSetDiff{pruningPoint: target, method: state.Method, toAdd: state.Added, toRemove: state.Removed}
		current, e := pm.pruningStore.PruningPoint(pm.databaseContext, model.NewStagingArea())
		lock.Unlock()
		if e != nil {
			return e
		}
		log.Infof("Pruning point %s: UTXO set written in %s (%s diff: %d added, %d removed); checking it against its commitment in the background", target, time.Since(start), state.Method, state.Added, state.Removed)
		if !current.Equal(target) {
			continue
		}
		// Scanner revalidates target and generation before mismatch or destructive finish.
		pm.journalHook("scan")
		testLockPhase(lock, "scan")
		if e = pm.scanPruningPointUTXOSet(ctx, lock); e != nil {
			return e
		}
		// A finished job needs none of its records: clear them now, off-lock, instead
		// of at the next Build, so an idle node keeps only the manifest.
		if e = pm.clearFinishedJournal(ctx); e != nil {
			return e
		}
	}
}

// clearFinishedJournal removes the journal records once the update they belong to has
// finished. Repeatable: an interrupted clear is completed here or by the next Build.
func (pm *pruningManager) clearFinishedJournal(ctx context.Context) error {
	if pm.pruningDiagnostics {
		started := time.Now()
		defer func() { log.Infof("Pruning diagnostics: phase=clearFinishedJournal elapsed=%s", time.Since(started)) }()
	}
	pending, err := pm.pruningStore.HadStartedUpdatingPruningPointUTXOSet(pm.databaseContext)
	if err != nil || pending {
		return err
	}
	return pm.journal.Clear(ctx)
}

// buildJournal derives and seals one job from the captured snapshot. The snapshot is
// released on every return path, including panics, before any later phase runs.
func (pm *pruningManager) buildJournal(ctx context.Context, state *pruningstore.JournalState,
	snapshot *database.ReadSnapshot, target *externalapi.DomainHash) (err error) {
	if pm.pruningDiagnostics {
		started := time.Now()
		defer func() { log.Infof("Pruning diagnostics: phase=buildJournal elapsed=%s", time.Since(started)) }()
	}
	defer func() {
		if closeErr := snapshot.Close(); err == nil {
			err = closeErr
		}
	}()
	pm.journalHook("derive")
	base, err := externalapi.NewDomainHashFromString(state.Base)
	if err != nil {
		return err
	}
	view, err := pm.readViewWithContext(ctx, snapshot, pm.journalPrefix, state.BaseIndex, base, target)
	if err != nil {
		return err
	}
	view.manager.setDerivationHook(pm.derivationTestHook())
	diff, method, err := view.diff()
	if err != nil {
		return err
	}
	return pm.journal.Build(ctx, state, diff, method)
}

// captureJournalJob holds the consensus lock only for identity/phase and snapshot
// capture; it writes nothing, so no fsync runs under the lock. Ready resumes exact
// sealed bytes. Preparing may restart derivation because the complete base is
// untouched. A next target is always the next selected index: then begin is true, the
// returned state is the COMPLETE base, and the caller saves PREPARING off-lock.
func (pm *pruningManager) captureJournalJob() (s *pruningstore.JournalState, snapshot *database.ReadSnapshot,
	target *externalapi.DomainHash, begin bool, e error) {
	pending, e := pm.pruningStore.HadStartedUpdatingPruningPointUTXOSet(pm.databaseContext)
	if e != nil || !pending {
		return nil, nil, nil, false, e
	}
	s, e = pm.journal.Load()
	if e != nil {
		return nil, nil, nil, false, e
	}
	if s == nil {
		return nil, nil, nil, false, fmt.Errorf("pruning journal: missing materialized position")
	}
	index, e := pm.pruningStore.CurrentPruningPointIndex(pm.databaseContext, model.NewStagingArea())
	if e != nil {
		return nil, nil, nil, false, e
	}
	if s.TargetIndex > index {
		return nil, nil, nil, false, fmt.Errorf("pruning journal: selected position regressed")
	}
	targetIndex, baseIndex, baseName := s.TargetIndex, s.BaseIndex, s.Base
	begin = s.Phase == "complete" && s.TargetIndex < index
	if begin {
		targetIndex, baseIndex, baseName = s.TargetIndex+1, s.TargetIndex, s.Target
	}
	target, e = pm.pruningStore.PruningPointByIndex(pm.databaseContext, model.NewStagingArea(), targetIndex)
	if e != nil {
		return nil, nil, nil, false, e
	}
	if !begin && target.String() != s.Target {
		return nil, nil, nil, false, fmt.Errorf("pruning journal: target identity changed")
	}
	if begin || s.Phase != "complete" {
		base, err := pm.pruningStore.PruningPointByIndex(pm.databaseContext, model.NewStagingArea(), baseIndex)
		if err != nil {
			return nil, nil, nil, false, err
		}
		if targetIndex != baseIndex+1 || base.String() != baseName {
			return nil, nil, nil, false, fmt.Errorf("pruning journal: base identity changed")
		}
	}
	if s.Phase == "complete" && !begin {
		return s, nil, target, false, nil
	}
	pm.appliedUTXOSetDiff = nil
	pm.journal.Invalidate()
	if s.Phase == "ready" {
		return s, nil, target, false, nil
	}
	snapshot, e = pm.databaseContext.(interface {
		NewSnapshot() (*database.ReadSnapshot, error)
	}).NewSnapshot()
	return s, snapshot, target, begin, e
}

// journalDiskFallback runs under the consensus lock after the journal refused to
// begin the next move for disk space. On a pruned node, and only for the move right
// after the COMPLETE base, it writes that move on the deferred legacy path: the diff
// once under the lock, as base does (base's lock cost for this move only), then the
// manifest COMPLETE at the new index. The usual off-lock scan and finish follow, and
// the finish deletes history, which frees the space the journal waits for. If the
// manifest write fails, the next attempt writes the diff again unless this process
// still records it as written; its writes are keyed by outpoint, so that is harmless.
//
// Further behind (an own backlog) it keeps waiting: the legacy diff is
// Δ(PP(index−1)→PP(index)) and fits the live set only at index−1, so applying it to
// an older set would end in a commitment mismatch. Archival nodes free no space by
// deleting history, so they keep waiting too. The refusal is returned unchanged for
// the scanner's rate-limited disk-wait log.
func (pm *pruningManager) journalDiskFallback(refusal error) (*pruningstore.JournalState, *externalapi.DomainHash, error) {
	if pm.isArchivalNode {
		return nil, nil, refusal
	}
	base, err := pm.journal.Load()
	if err != nil {
		return nil, nil, err
	}
	index, err := pm.pruningStore.CurrentPruningPointIndex(pm.databaseContext, model.NewStagingArea())
	if err != nil {
		return nil, nil, err
	}
	if base == nil || base.Phase != "complete" || base.TargetIndex+1 != index {
		return nil, nil, refusal
	}
	target, err := pm.pruningStore.PruningPointByIndex(pm.databaseContext, model.NewStagingArea(), index)
	if err != nil {
		return nil, nil, err
	}
	applied := pm.appliedUTXOSetDiff
	if applied == nil || !applied.pruningPoint.Equal(target) {
		log.Warnf("Pruning point %s: the UTXO journal cannot begin this move for lack of disk space (%s); the move "+
			"is written on the synchronous path instead, so its finish can delete history and free space", target, refusal)
		if applied, err = pm.applyPruningPointUTXOSetDiff(model.NewStagingArea(), target); err != nil {
			return nil, nil, err
		}
		pm.appliedUTXOSetDiff = applied
	}
	state, err := pm.journal.AdoptSynchronousMove(base, target, applied.method, applied.toAdd, applied.toRemove)
	if err != nil {
		return nil, nil, err
	}
	return state, target, nil
}

// FinishJournalDowngrade is called only with admissions excluded and the worker joined.
func FinishJournalDowngrade(manager model.PruningManager, ctx context.Context) error {
	pm := manager.(*pruningManager)
	if pm.journal == nil {
		return nil
	}
	if e := pm.journal.RemoveAfterFinish(ctx); e != nil {
		return e
	}
	pm.journal = nil
	return nil
}

func (pm *pruningManager) journalHook(phase string) { pm.journal.TestHook(phase) }
