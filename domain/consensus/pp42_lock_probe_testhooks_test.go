//go:build pruningtest

package consensus

import (
	"fmt"
	"time"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/database"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/processes/pruningmanager"
	"github.com/HoosatNetwork/HTND/v2/util/staging"
)

// ConsensusLockHeld reports whether some goroutine holds the consensus lock of c.
// Tests call it from an owner's hook while nothing else can hold the lock.
func ConsensusLockHeld(c externalapi.Consensus) bool {
	l := c.(*consensus).lock
	if l.TryLock() {
		l.Unlock()
		return false
	}
	return true
}

// RunLegacyHistoryDeletionForTest runs pruningmanager.RunLegacyHistoryDeletion on c.
// Call it from a "history-recorded" hook, where the consensus lock is already held.
func RunLegacyHistoryDeletionForTest(c externalapi.Consensus) error {
	return pruningmanager.RunLegacyHistoryDeletion(c.(*consensus).pruningManager)
}

// HistoryDeletionPlanForTest returns the live plan of c's lowest recorded history
// deletion job, under the consensus lock.
func HistoryDeletionPlanForTest(c externalapi.Consensus) ([]*externalapi.DomainHash, uint64, bool, error) {
	s := c.(*consensus)
	s.lock.Lock()
	defer s.lock.Unlock()
	return pruningmanager.HistoryDeletionPlan(s.pruningManager)
}

// SetHistoryDeletionRetryForTest shortens the history deletion owner's retry backoff
// and returns the function restoring it.
func SetHistoryDeletionRetryForTest(minimum, maximum time.Duration) func() {
	oldMin, oldMax := historyDeletionRetryMin, historyDeletionRetryMax
	historyDeletionRetryMin, historyDeletionRetryMax = minimum, maximum
	return func() { historyDeletionRetryMin, historyDeletionRetryMax = oldMin, oldMax }
}

// PruningManagerInstanceForTest is the instance id c's pruning manager logs under.
func PruningManagerInstanceForTest(c externalapi.Consensus) string {
	return fmt.Sprintf("%p", c.(*consensus).pruningManager)
}

// HistoryMaskBlocksForTest returns the blocks c's published history mask masks, sorted by
// hash, and whether it masks their data; nil without a published mask.
func HistoryMaskBlocksForTest(c externalapi.Consensus) ([]*externalapi.DomainHash, bool) {
	return pruningmanager.HistoryMaskBlocks(c.(*consensus).pruningManager)
}

// HistoryView is what readers see of one block in the stores a history deletion touches:
// under the lock through the cached getters (fresh staging area), and through the
// lock-free paths. A field is false where the read found nothing.
type HistoryView struct {
	Status                                                    externalapi.BlockStatus
	StatusExists, HasBlock, Block, AcceptanceData, Multiset   bool
	UTXODiff, UTXODiffChild, HasUTXODiffChild, DAAAddedBlocks bool
	DAAScore                                                  bool
	LockFreeStatus                                            externalapi.BlockStatus
	LockFreeStatusExists, LockFreeBlock, LockFreeHasBlock     bool
}

// HistoryViewForTest reads blockHash from every store a history deletion touches, as a
// locked reader and as a lock-free one. Not-found reads are false; any other error fails.
func HistoryViewForTest(c externalapi.Consensus, blockHash *externalapi.DomainHash) (HistoryView, error) {
	s := c.(*consensus)
	var v HistoryView
	found := func(e error) (bool, error) {
		if e == nil {
			return true, nil
		}
		if database.IsNotFoundError(e) {
			return false, nil
		}
		return false, e
	}
	var e error
	err := func() error {
		s.lock.Lock()
		defer s.lock.Unlock()
		stagingArea := model.NewStagingArea()
		if v.StatusExists, e = s.blockStatusStore.Exists(s.databaseContext, stagingArea, blockHash); e != nil {
			return e
		}
		status, e := s.blockStatusStore.Get(s.databaseContext, stagingArea, blockHash)
		if ok, e := found(e); e != nil {
			return e
		} else if ok {
			v.Status = status
		}
		if v.HasBlock, e = s.blockStore.HasBlock(s.databaseContext, stagingArea, blockHash); e != nil {
			return e
		}
		_, e = s.blockStore.Block(s.databaseContext, stagingArea, blockHash)
		if v.Block, e = found(e); e != nil {
			return e
		}
		_, e = s.acceptanceDataStore.Get(s.databaseContext, stagingArea, blockHash)
		if v.AcceptanceData, e = found(e); e != nil {
			return e
		}
		_, e = s.multisetStore.Get(s.databaseContext, stagingArea, blockHash)
		if v.Multiset, e = found(e); e != nil {
			return e
		}
		_, e = s.utxoDiffStore.UTXODiff(s.databaseContext, stagingArea, blockHash)
		if v.UTXODiff, e = found(e); e != nil {
			return e
		}
		child, e := s.utxoDiffStore.UTXODiffChild(s.databaseContext, stagingArea, blockHash)
		if e != nil {
			return e
		}
		v.UTXODiffChild = child != nil
		if v.HasUTXODiffChild, e = s.utxoDiffStore.HasUTXODiffChild(s.databaseContext, stagingArea, blockHash); e != nil {
			return e
		}
		_, e = s.daaBlocksStore.DAAAddedBlocks(s.databaseContext, stagingArea, blockHash)
		if v.DAAAddedBlocks, e = found(e); e != nil {
			return e
		}
		_, e = s.daaBlocksStore.DAAScore(s.databaseContext, stagingArea, blockHash)
		v.DAAScore, e = found(e)
		return e
	}()
	if err != nil {
		return v, err
	}
	if v.LockFreeStatus, v.LockFreeStatusExists, e = s.blockStatusStore.GetWithoutCaching(s.databaseContext, blockHash); e != nil {
		return v, e
	}
	_, e = s.blockStore.BlockWithoutCaching(s.databaseContext, blockHash)
	if v.LockFreeBlock, e = found(e); e != nil {
		return v, e
	}
	v.LockFreeHasBlock, e = s.blockStore.HasBlockWithoutCaching(s.databaseContext, blockHash)
	return v, e
}

// FindHighestValidBlockForTest runs the zero-tips fallback of calculateNewTips under the
// consensus lock.
func FindHighestValidBlockForTest(c externalapi.Consensus) (*externalapi.DomainHash, error) {
	s := c.(*consensus)
	s.lock.Lock()
	defer s.lock.Unlock()
	return s.consensusStateManager.(interface {
		FindHighestValidBlock() (*externalapi.DomainHash, error)
	}).FindHighestValidBlock()
}

// RepairDisqualifiedChainsFromForTest runs RepairDisqualifiedTipChains' walk from starts
// instead of the tips, under the consensus lock.
func RepairDisqualifiedChainsFromForTest(c externalapi.Consensus, starts []*externalapi.DomainHash) (uint64, error) {
	s := c.(*consensus)
	s.lock.Lock()
	defer s.lock.Unlock()
	return s.repairDisqualifiedChains(model.NewStagingArea(), starts)
}

// StageStatusForTest commits status as blockHash's status, under the consensus lock.
func StageStatusForTest(c externalapi.Consensus, blockHash *externalapi.DomainHash, status externalapi.BlockStatus) error {
	s := c.(*consensus)
	s.lock.Lock()
	defer s.lock.Unlock()
	stagingArea := model.NewStagingArea()
	s.blockStatusStore.Stage(stagingArea, blockHash, status)
	return staging.CommitAllChanges(s.databaseContext, stagingArea)
}

// PruningCancellationForTest signals after Close cancels the paused journal owner.
func PruningCancellationForTest(c externalapi.Consensus) <-chan struct{} {
	s := c.(*consensus)
	called := make(chan struct{})
	cancel := s.pruningPointUTXOSetScanner.cancel
	s.pruningPointUTXOSetScanner.cancel = func() { cancel(); close(called) }
	return called
}
