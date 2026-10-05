package templatemanager

import (
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/pow"
)

// Job is an immutable unit of work handed to the mining threads. A thread copies State once
// per job and then only touches its own copy, so the hot loop takes no locks.
type Job struct {
	ID       uint64
	Block    *externalapi.DomainBlock
	State    *pow.State
	IsSynced bool
}

var (
	lock = &sync.Mutex{}

	current    *Job
	parentsKey string

	// solvedParentsKey is set while a block found on a template with these parents is being
	// submitted or has been accepted. Mining another block on the same parents would only produce
	// a sibling of our own block: it can never join the selected chain alongside it and ends up
	// UTXO-pending-verification (or red) with a duplicate copy of the same transactions.
	solvedParentsKey string

	// notBefore enforces the minimum spacing between solutions set by SetMinSolveInterval.
	notBefore        time.Time
	minSolveInterval time.Duration

	changed = make(chan struct{})

	// generation is bumped whenever the job threads should be working on changes, so a thread can
	// detect a stale job with a single atomic load per hash.
	generation atomic.Uint64
)

// Generation returns the ID of the job threads should currently be working on.
func Generation() uint64 {
	return generation.Load()
}

// SetMinSolveInterval sets the minimum time between two solutions. Zero means no limit.
func SetMinSolveInterval(interval time.Duration) {
	lock.Lock()
	defer lock.Unlock()
	minSolveInterval = interval
}

// Current returns the job to mine, or nil if there is nothing to mine right now, together with a
// channel that is closed on the next state change and the earliest time mining may resume.
func Current() (job *Job, changedChan <-chan struct{}, resumeAt time.Time) {
	lock.Lock()
	defer lock.Unlock()
	if current == nil || parentsKey == solvedParentsKey {
		return nil, changed, time.Time{}
	}
	if time.Now().Before(notBefore) {
		return nil, changed, notBefore
	}
	return current, changed, time.Time{}
}

// HasTemplate returns whether any template was received yet, and whether the node reported itself synced.
func HasTemplate() (hasTemplate bool, isSynced bool) {
	lock.Lock()
	defer lock.Unlock()
	if current == nil {
		return false, false
	}
	return true, current.IsSynced
}

// Set sets the current template to work on
func Set(template *appmessage.GetBlockTemplateResponseMessage) error {
	block, err := appmessage.RPCBlockToDomainBlock(template.Block, "TEMPLATE_POW_HASH")
	if err != nil {
		return err
	}
	newParentsKey := keyOfParents(block.Header.DirectParents())

	lock.Lock()
	defer lock.Unlock()

	// The template is polled far more often than it changes. Restarting the threads on an
	// identical template would only throw away their nonce progress and state copies.
	if current != nil && current.IsSynced == template.IsSynced &&
		current.Block.Header.Equal(block.Header) {
		return nil
	}

	current = &Job{
		ID:       generation.Load() + 1,
		Block:    block,
		State:    pow.NewState(block.Header.ToMutable()),
		IsSynced: template.IsSynced,
	}
	parentsKey = newParentsKey
	if solvedParentsKey != "" && solvedParentsKey != parentsKey {
		solvedParentsKey = ""
	}
	bumpLocked()
	return nil
}

// MarkSolved claims the solution for the given job. It returns false if the job is no longer
// current or another thread already solved it, in which case the block must be dropped because
// it would be a sibling of a block we already found.
func MarkSolved(jobID uint64) bool {
	lock.Lock()
	defer lock.Unlock()
	if current == nil || current.ID != jobID || parentsKey == solvedParentsKey {
		return false
	}
	solvedParentsKey = parentsKey
	if minSolveInterval > 0 {
		notBefore = time.Now().Add(minSolveInterval)
	}
	bumpLocked()
	return true
}

// SubmitFailed releases the parents claimed by MarkSolved so mining can resume on them.
// After a successful submit the claim is kept until a template with different parents arrives,
// which the node produces as soon as it has added our block.
func SubmitFailed(block *externalapi.DomainBlock) {
	lock.Lock()
	defer lock.Unlock()
	if solvedParentsKey != keyOfParents(block.Header.DirectParents()) {
		return
	}
	solvedParentsKey = ""
	if current != nil {
		// Give the job a new ID so the thread that solved it picks it up again.
		current = &Job{ID: generation.Load() + 1, Block: current.Block, State: current.State, IsSynced: current.IsSynced}
	}
	bumpLocked()
}

func bumpLocked() {
	if current != nil && current.ID > generation.Load() {
		generation.Store(current.ID)
	} else {
		generation.Add(1)
	}
	close(changed)
	changed = make(chan struct{})
}

func keyOfParents(parents []*externalapi.DomainHash) string {
	var sb strings.Builder
	for _, parent := range parents {
		sb.WriteString(parent.String())
	}
	return sb.String()
}
