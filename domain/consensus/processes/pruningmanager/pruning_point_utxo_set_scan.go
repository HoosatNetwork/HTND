package pruningmanager

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/pkg/errors"
)

// ErrPruningPointUTXOSetMismatch is returned by ScanPruningPointUTXOSet when the pruning point UTXO set does not
// hash to the commitment in the pruning point's header. The node must stop: the set is wrong, and it is neither
// served nor used to delete past blocks.
var ErrPruningPointUTXOSetMismatch = errors.New("[PP-COMMITMENT] HALTING: the pruning point UTXO set this node " +
	"computed does not match the chain")

// applyPruningPointUTXOSetDiffAndRequestScan is UpdatePruningPointIfRequired with
// Config.DeferPruningPointUTXOSetUpdate. It writes the pending diff into the pruning point UTXO set, once per pruning
// point, and requests ScanPruningPointUTXOSet, which hashes the set without the consensus lock, then compares the
// commitment and finishes the update under the lock. Until then the update stays pending, so the set is not served.
func (pm *pruningManager) applyPruningPointUTXOSetDiffAndRequestScan() error {
	stagingArea := model.NewStagingArea()
	pruningPoint, err := pm.pruningStore.PruningPoint(pm.databaseContext, stagingArea)
	if err != nil {
		return err
	}
	if pm.appliedUTXOSetDiff == nil || !pm.appliedUTXOSetDiff.pruningPoint.Equal(pruningPoint) {
		log.Infof("Pruning point UTXO set update is required")
		start := time.Now()
		applied, err := pm.applyPruningPointUTXOSetDiff(stagingArea, pruningPoint)
		if err != nil {
			return err
		}
		if pruningPoint.Equal(pm.genesisHash) {
			// Nothing to check against a commitment, as in updatePruningPoint.
			return pm.finishPruningPointUTXOSetUpdate(stagingArea, pruningPoint)
		}
		pm.appliedUTXOSetDiff = applied
		log.Infof("Pruning point %s: UTXO set written in %s (%s diff: %d added, %d removed); checking it against "+
			"its commitment in the background", pruningPoint, time.Since(start), applied.method, applied.toAdd,
			applied.toRemove)
	}
	pm.requestUTXOSetScan()
	return nil
}

// ScanPruningPointUTXOSet finishes a pruning point UTXO set update that applyPruningPointUTXOSetDiffAndRequestScan
// left pending: it hashes the set and compares it with the pruning point's header commitment, then deletes the past
// blocks and clears the pending flag in one commit. lock is the consensus lock. It is held only to open the iterator
// and to finish, not for the walk, which takes minutes on mainnet.
//
// The iterator is opened under the lock. Legacy writers hold that lock; the journal
// owner instead requires COMPLETE admission and invalidates its generation under the
// same lock before any off-lock write. Thus an admitted iterator always sees a complete set.
// A set written again, or a pruning point moved before the walk ends, makes the result stale. It is dropped, whatever it was, and the current
// set is scanned when its own diff has been written.
//
// It returns an error wrapping ErrPruningPointUTXOSetMismatch, with a bounded diagnostic, if the set does not match.
// Any other error leaves the update pending, and the next UpdatePruningPointIfRequired requests the scan again. If ctx
// is cancelled, it returns early and leaves the update pending, so the next start scans again.
func (pm *pruningManager) ScanPruningPointUTXOSet(ctx context.Context, lock sync.Locker) error {
	if pm.journal != nil {
		return pm.RunJournalWorker(ctx, lock)
	}
	return pm.scanPruningPointUTXOSet(ctx, lock)
}

func (pm *pruningManager) scanPruningPointUTXOSet(ctx context.Context, lock sync.Locker) (resultErr error) {
	lock.Lock()
	pruningPoint, utxoSetIterator, generation, err := pm.openPruningPointUTXOSetScan()
	lock.Unlock()
	if err != nil || utxoSetIterator == nil {
		return err
	}

	start := time.Now()
	utxoSetHash, stats, err := hashUTXOSet(utxoSetIterator, func() bool {
		return ctx.Err() != nil || pm.pruningStore.PruningPointUTXOSetGeneration() != generation
	})
	closeErr := utxoSetIterator.Close()
	stopped := errors.Is(err, errUTXOSetScanStopped)

	if err != nil && !stopped {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	elapsed := time.Since(start)

	lock.Lock()
	defer lock.Unlock()
	if ctx.Err() != nil {
		return nil
	}
	stagingArea := model.NewStagingArea()
	currentPruningPoint, err := pm.pruningStore.PruningPoint(pm.databaseContext, stagingArea)
	if err != nil {
		return err
	}
	if stopped || !currentPruningPoint.Equal(pruningPoint) ||
		pm.pruningStore.PruningPointUTXOSetGeneration() != generation {

		log.Warnf("[PP-COMMITMENT] The check of pruning point %s's UTXO set against its commitment was superseded by "+
			"pruning point %s after %s and %d entries (walk complete: %t). Its result is dropped; pruning point %s's "+
			"set is checked once its diff is written. While the node catches up (IBD, or after downtime) several "+
			"pruning point moves in a row are expected. On a synced node they are not: pruning points move a few "+
			"times a day and the check takes minutes, so report this if the node was synced.",
			pruningPoint, currentPruningPoint, elapsed, stats.entries, !stopped, currentPruningPoint)
		return nil
	}

	header, err := pm.blockHeaderStore.BlockHeader(pm.databaseContext, stagingArea, pruningPoint)
	if err != nil {
		return err
	}
	if !header.UTXOCommitment().Equal(utxoSetHash) {
		return pm.pruningPointUTXOSetMismatchError(stagingArea, pruningPoint, header, utxoSetHash, stats)
	}
	log.Infof("Pruning point %s: the UTXO set this node serves matches the chain's commitment for it "+
		"(checked in %s over %s; hash %s)", pruningPoint, elapsed, stats, utxoSetHash)

	return pm.finishPruningPointUTXOSetUpdate(stagingArea, pruningPoint)
}

// openPruningPointUTXOSetScan returns the pruning point, an iterator over its UTXO set and the set's generation, or a
// nil iterator if there is nothing to scan: no update is pending, or the diff of the current pruning point is not
// written yet (the set is still its predecessor's, and writing the diff requests the scan again). The caller must
// hold the consensus lock, and close the iterator.
func (pm *pruningManager) openPruningPointUTXOSetScan() (
	*externalapi.DomainHash, externalapi.ReadOnlyUTXOSetIterator, uint64, error,
) {
	updating, err := pm.pruningStore.HadStartedUpdatingPruningPointUTXOSet(pm.databaseContext)
	if err != nil || !updating {
		return nil, nil, 0, err
	}
	pruningPoint, err := pm.pruningStore.PruningPoint(pm.databaseContext, model.NewStagingArea())
	if err != nil {
		return nil, nil, 0, err
	}
	if pm.appliedUTXOSetDiff == nil || !pm.appliedUTXOSetDiff.pruningPoint.Equal(pruningPoint) {
		return nil, nil, 0, nil
	}
	// COMPLETE is mandatory even if a cached applied diff still names this hash.
	if pm.journal != nil {
		state, err := pm.journal.Load()
		if err != nil {
			return nil, nil, 0, err
		}
		if state == nil || state.Phase != "complete" || state.Target != pruningPoint.String() {
			return nil, nil, 0, nil
		}
	}
	generation := pm.pruningStore.PruningPointUTXOSetGeneration()
	utxoSetIterator, err := pm.pruningStore.PruningPointUTXOIterator(pm.databaseContext)
	if err != nil {
		return nil, nil, 0, err
	}
	return pruningPoint, utxoSetIterator, generation, nil
}

// pruningPointUTXOSetMismatchError describes a pruning point UTXO set that does not match its commitment, from what
// the node has at hand. It is bounded: the full report (reportPruningPointCommitmentMismatch) walks the chain and
// derives the diff again, which takes minutes, and runs with --enable-utxo-debug-diagnostics.
func (pm *pruningManager) pruningPointUTXOSetMismatchError(stagingArea *model.StagingArea,
	pruningPoint *externalapi.DomainHash, header externalapi.BlockHeader, utxoSetHash *externalapi.DomainHash,
	stats *utxoSetStats,
) error {
	expected := header.UTXOCommitment()
	index := "unknown"
	previousPruningPoint := "unknown"
	previousClean := false
	if pruningPointIndex, err := pm.pruningStore.CurrentPruningPointIndex(pm.databaseContext, stagingArea); err == nil {
		index = fmt.Sprint(pruningPointIndex)
		if pruningPointIndex > 0 {
			previous, err := pm.pruningStore.PruningPointByIndex(pm.databaseContext, stagingArea, pruningPointIndex-1)
			if err == nil {
				previousPruningPoint = previous.String()
				previousHeader, err := pm.blockHeaderStore.BlockHeader(pm.databaseContext, stagingArea, previous)
				previousPerBlock := pm.perBlockMultisetHash(stagingArea, previous)
				previousClean = err == nil && previousPerBlock != nil &&
					previousPerBlock.Equal(previousHeader.UTXOCommitment())
			}
		}
	}
	perBlock := pm.perBlockMultisetHash(stagingArea, pruningPoint)
	applied := pm.appliedUTXOSetDiff

	return fmt.Errorf("%w\n"+
		"  pruning point      %s (index %s, DAA score %d, blue score %d, header version %d)\n"+
		"  header commitment  %s\n"+
		"  computed hash      %s over %s\n"+
		"  set derived by     %s diff from previous pruning point %s (%d entries added, %d removed)\n"+
		"  per-block multiset %s (matches header: %t, matches the computed set: %t)\n"+
		"  %s\n"+
		"  The set was not served to any peer and no blocks were deleted; the data is left in place.\n"+
		"  A restart writes and checks the set again (minutes) and stops again if it still does not match.\n"+
		"  Run with --enable-utxo-debug-diagnostics for the full report (chain walk, diff comparison), or resync\n"+
		"  from scratch.",
		ErrPruningPointUTXOSetMismatch,
		pruningPoint, index, header.DAAScore(), header.BlueScore(), header.Version(),
		expected,
		utxoSetHash, stats,
		applied.method, previousPruningPoint, applied.toAdd, applied.toRemove,
		perBlock, perBlock != nil && perBlock.Equal(expected), perBlock != nil && perBlock.Equal(utxoSetHash),
		classifyPruningPointMismatch(previousClean, perBlock, expected, utxoSetHash))
}
