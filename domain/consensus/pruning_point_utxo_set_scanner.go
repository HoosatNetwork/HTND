package consensus

import (
	"context"
	"sync"
	"time"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/datastructures/pruningstore"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/processes/pruningmanager"
	"github.com/pkg/errors"
)

// pruningPointUTXOSetScanner runs, with Config.DeferPruningPointUTXOSetUpdate, the check of the pruning point UTXO
// set against its commitment that finishes a pruning point UTXO set update (PruningManager.ScanPruningPointUTXOSet).
// It runs on its own goroutine; only the full-set hash walk runs without the consensus lock. One check runs at a
// time, and the requests made meanwhile coalesce into one more.
type pruningPointUTXOSetScanner struct {
	requests   chan struct{}
	onMismatch func(error)

	cancel    context.CancelFunc
	done      chan struct{}
	closeOnce sync.Once
}

func (s *consensus) requestPruningPointUTXOSetScan() {
	select {
	case s.pruningPointUTXOSetScanner.requests <- struct{}{}:
	default:
	}
}

func (s *consensus) startPruningPointUTXOSetScanner() {
	scanner := &s.pruningPointUTXOSetScanner
	ctx, cancel := context.WithCancel(context.Background())
	scanner.cancel = cancel
	scanner.done = make(chan struct{})
	spawn("pruningPointUTXOSetScanner", func() {
		defer close(scanner.done)
		diskWait := diskWaitLog{now: time.Now}
		for {
			select {
			case <-ctx.Done():
				return
			case <-scanner.requests:
			}
			err := s.pruningManager.ScanPruningPointUTXOSet(ctx, s.lock)
			if errors.Is(err, pruningmanager.ErrPruningPointUTXOSetMismatch) {
				log.Criticalf("%s", err)
				if scanner.onMismatch == nil {
					// Never keep running on a set that does not match the chain.
					panic(err)
				}
				scanner.onMismatch(err)
				return
			}
			if errors.Is(err, pruningstore.ErrJournalDiskReserve) {
				if diskWait.refused() {
					log.Warnf("The pruning point UTXO journal job waits for disk space; it is retried after each block "+
						"and this is logged at most once per %s: %s", diskWaitLogInterval, err)
				}
				continue
			}
			if diskWait.resumed() {
				log.Infof("The pruning point UTXO journal job is no longer waiting for disk space")
			}
			if err != nil {
				log.Errorf("The pruning point UTXO set check failed, and is retried after the next block: %+v", err)
			}
		}
	})
}

// diskWaitLogInterval bounds how often a job waiting for disk space is logged.
const diskWaitLogInterval = time.Minute

// diskWaitLog rate-limits the log of a journal job waiting for disk space. The scanner
// retries it after every block; the refusal is logged at most once per
// diskWaitLogInterval, and the end of the wait once.
type diskWaitLog struct {
	now     func() time.Time
	last    time.Time
	waiting bool
}

// refused reports whether this refusal is logged.
func (w *diskWaitLog) refused() bool {
	now := w.now()
	if w.waiting && now.Sub(w.last) < diskWaitLogInterval {
		return false
	}
	w.waiting, w.last = true, now
	return true
}

// resumed reports whether a wait ended with this attempt.
func (w *diskWaitLog) resumed() bool {
	ended := w.waiting
	w.waiting = false
	return ended
}

// Close stops the pruning point UTXO set check and the history deletion owner and waits for both to return, so the
// database can be closed after it. A check it interrupts leaves the update pending, and the next start checks the set
// again; an interrupted history deletion job keeps its record and resumes at the next start.
func (s *consensus) Close() {
	scanner := &s.pruningPointUTXOSetScanner
	scanner.closeOnce.Do(func() {
		if scanner.cancel != nil {
			scanner.cancel()
			<-scanner.done
		}
	})
	// After the scanner: its finish may still hand the owner a job.
	s.historyDeletionOwner.close()
}
