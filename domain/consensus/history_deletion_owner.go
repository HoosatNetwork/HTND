package consensus

import (
	"context"
	"sync"
	"time"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/processes/pruningmanager"
)

// Retry backoff of a failed history deletion job : from historyDeletionRetryMin, doubling up to
// historyDeletionRetryMax. Variables only so tests can shorten them.
var historyDeletionRetryMin, historyDeletionRetryMax = time.Minute, 10 * time.Minute

// historyDeletionOwner runs the history deletion jobs that pruning point updates record
// (pruningmanager.RunHistoryDeletionJobs) on its own goroutine, one at a time. Requests made meanwhile coalesce
// into one more run. A failed job keeps its record and every finished batch, and is retried with a backoff; the
// node keeps processing blocks meanwhile.
type historyDeletionOwner struct {
	requests chan struct{}

	cancel    context.CancelFunc
	done      chan struct{}
	closeOnce sync.Once
}

func (s *consensus) requestHistoryDeletion() {
	select {
	case s.historyDeletionOwner.requests <- struct{}{}:
	default:
	}
}

func (s *consensus) startHistoryDeletionOwner() {
	owner := &s.historyDeletionOwner
	ctx, cancel := context.WithCancel(context.Background())
	owner.cancel = cancel
	owner.done = make(chan struct{})
	spawn("historyDeletionOwner", func() {
		defer close(owner.done)
		retry := historyDeletionRetryMin
		var retryAt <-chan time.Time
		for {
			select {
			case <-ctx.Done():
				return
			case <-owner.requests:
			case <-retryAt:
			}
			err := pruningmanager.RunHistoryDeletionJobs(s.pruningManager, ctx, s.lock)
			if err == nil || ctx.Err() != nil {
				retry, retryAt = historyDeletionRetryMin, nil
				continue
			}
			log.Warnf("History deletion below the pruning point failed; the job keeps its progress and is retried "+
				"in %s: %s", retry, err)
			retryAt = time.After(retry)
			retry = min(2*retry, historyDeletionRetryMax)
		}
	})
}

// close stops the owner and waits for it to return. A job it interrupts resumes at the next start.
func (owner *historyDeletionOwner) close() {
	owner.closeOnce.Do(func() {
		if owner.cancel != nil {
			owner.cancel()
			<-owner.done
		}
	})
}
