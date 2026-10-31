package database

import (
	"sync/atomic"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
)

// HistoryMask is the logical delete of a running history deletion job: the blocks the job
// deletes, published in one consensus lock hold before its first batch commits, and removed
// in a hold after its last batch has been evicted from the caches. The stores a deletion touches
// consult it after their staging shard and before their cache, in the readers that hold the
// consensus lock and in the lock-free ones alike. A masked block reads as header-only in the
// status store and as not found in every key the deletion removes; the DAA score, which the
// deletion keeps, is not masked. On an archival node, which keeps the data, only the status
// is masked. From its publication on, these point reads see the job's end state, as base
// showed it after its single locked deletion commit, while the batches delete the keys behind
// it without the lock. Iterators are not masked: the block store's AllBlockHashesIterator
// still yields a masked block until its batch deletes the body. A published set is never
// changed, so lock-free readers need no lock.
type HistoryMask struct {
	reads historyMaskReads
	set   atomic.Pointer[HistoryMaskSet]
}

// HistoryMaskSet is an immutable set of masked blocks. Build it without the consensus lock.
type HistoryMaskSet struct {
	blocks map[externalapi.DomainHash]struct{}
	data   bool
}

// HistoryMaskReader identifies which store is being read.
type HistoryMaskReader int

// The stores that consult a HistoryMask.
const (
	HistoryMaskStatus HistoryMaskReader = iota
	HistoryMaskBlocks
	HistoryMaskAcceptanceData
	HistoryMaskMultisets
	HistoryMaskUTXODiffs
	HistoryMaskDAAAddedBlocks
	HistoryMaskReaders
)

// NewHistoryMaskSet builds the set masking blockHashes. withData also masks their deleted
// data; an archival node masks the status only.
func NewHistoryMaskSet(blockHashes []*externalapi.DomainHash, withData bool) *HistoryMaskSet {
	blocks := make(map[externalapi.DomainHash]struct{}, len(blockHashes))
	for _, blockHash := range blockHashes {
		blocks[*blockHash] = struct{}{}
	}
	return &HistoryMaskSet{blocks: blocks, data: withData}
}

// Publish replaces the masked set with set; nil removes the mask. Call under the consensus
// lock: a reader holding it must see one set for the whole hold. A nil mask publishes
// nothing.
func (m *HistoryMask) Publish(set *HistoryMaskSet) {
	if m != nil {
		m.set.Store(set)
	}
}

// Masks reports whether reader must answer blockHash as deleted.
// A nil mask masks nothing.
func (m *HistoryMask) Masks(reader HistoryMaskReader, blockHash *externalapi.DomainHash) bool {
	if m == nil {
		return false
	}
	set := m.set.Load()
	if set == nil || (reader != HistoryMaskStatus && !set.data) {
		return false
	}
	if _, ok := set.blocks[*blockHash]; !ok {
		return false
	}
	m.reads.count(reader)
	return true
}

// MaskedDataNotFound is the error a masked data read returns: the not-found error the
// database returns once the key is deleted.
func MaskedDataNotFound(what string, blockHash *externalapi.DomainHash) error {
	return &maskedNotFound{what + " " + blockHash.String() + " is being deleted with the history below the pruning point"}
}

type maskedNotFound struct{ message string }

func (e *maskedNotFound) Error() string { return e.message }
func (e *maskedNotFound) Unwrap() error { return ErrNotFound }
