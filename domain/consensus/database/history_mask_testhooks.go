//go:build pruningtest

package database

import (
	"slices"
	"sync/atomic"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
)

// historyMaskReads counts, per store, the masked reads of callers holding the consensus lock.
type historyMaskReads [HistoryMaskReaders]atomic.Uint64

func (r *historyMaskReads) count(reader HistoryMaskReader) { r[reader].Add(1) }

// TakeReads returns the masked reads under the lock counted since the last call, per store
// in HistoryMaskReader order, and restarts the count.
func (m *HistoryMask) TakeReads() (reads [HistoryMaskReaders]uint64) {
	for i := range reads {
		reads[i] = m.reads[i].Swap(0)
	}
	return reads
}

// BlocksForTest returns the published blocks, sorted, and whether their data is masked;
// nil without a published mask.
func (m *HistoryMask) BlocksForTest() (blocks []*externalapi.DomainHash, withData bool) {
	set := m.set.Load()
	if set == nil {
		return nil, false
	}
	for blockHash := range set.blocks {
		blocks = append(blocks, &blockHash)
	}
	slices.SortFunc(blocks, func(a, b *externalapi.DomainHash) int {
		if a.Less(b) {
			return -1
		}
		if b.Less(a) {
			return 1
		}
		return 0
	})
	return blocks, set.data
}
