package blockstore

import (
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/database"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
)

// DeleteBlockDataDirect writes into dbTx the delete a staging shard commit writes for
// a deleted block, without a staging area and without touching the caches.
func (bs *blockStore) DeleteBlockDataDirect(dbTx model.DBWriter, blockHash *externalapi.DomainHash) error {
	return dbTx.Delete(bs.hashAsKey(blockHash))
}

// PutDeletionCountsDirect writes into dbTx blocks-deleted after deletedBlocks more bodies
// are deleted, in the batch that deletes them. It writes no other count: blocks-count and
// blocks-count-deleted belong to the foreground's commits, which may run while the batch
// commits. Only the history deletion job deletes bodies, so deletedCount changes only in
// its own EvictDeleted between its batches.
func (bs *blockStore) PutDeletionCountsDirect(dbTx model.DBWriter, deletedBlocks uint64) error {
	bs.lock.Lock()
	deleted := bs.deletedCount + deletedBlocks
	bs.lock.Unlock()
	deletedBytes, err := bs.serializeBlockCount(deleted)
	if err != nil {
		return err
	}
	return dbTx.Put(bs.deletedKey, deletedBytes)
}

// RewriteStaleLegacyCountDirect writes into dbTx the exact blocks-count, and the deletions
// it reflects, if startup found it behind deletions (history deletion batches after the
// last block commit) or recounted it, and reports whether it did, so the previous release
// finds an exact count too. Call at startup, before anything else commits.
func (bs *blockStore) RewriteStaleLegacyCountDirect(dbTx model.DBWriter) (bool, error) {
	bs.lock.Lock()
	stale, count, deleted := bs.legacyCountStale, bs.countCached, bs.deletedCount
	bs.legacyCountStale = false
	bs.lock.Unlock()
	if !stale {
		return false, nil
	}
	countBytes, err := bs.serializeBlockCount(count)
	if err != nil {
		return false, err
	}
	deletedBytes, err := bs.serializeBlockCount(deleted)
	if err != nil {
		return false, err
	}
	if err = dbTx.Put(bs.countKey, countBytes); err != nil {
		return false, err
	}
	return true, dbTx.Put(bs.countedKey, deletedBytes)
}

// NormalizeCountsForDowngradeDirect writes into dbTx the exact legacy blocks-count and
// deletes blocks-count-deleted and blocks-deleted, which the previous release does not
// keep: a later start of this binary then takes the count from blocks-count, whatever the
// previous release did in between. It forgets the deletions in memory too. Call under
// the consensus lock, with no history deletion job left, and commit dbTx right after.
func (bs *blockStore) NormalizeCountsForDowngradeDirect(dbTx model.DBWriter) error {
	bs.lock.Lock()
	count := bs.countCached
	bs.deletedCount = 0
	bs.lock.Unlock()
	countBytes, err := bs.serializeBlockCount(count)
	if err != nil {
		return err
	}
	if err = dbTx.Put(bs.countKey, countBytes); err != nil {
		return err
	}
	if err = dbTx.Delete(bs.countedKey); err != nil {
		return err
	}
	return dbTx.Delete(bs.deletedKey)
}

// IsHistoryDataMasked reports whether blockHash's body is being deleted by the running
// history deletion job, so it reads as not found while it may still be stored. The
// caller holds the consensus lock.
func (bs *blockStore) IsHistoryDataMasked(blockHash *externalapi.DomainHash) bool {
	return bs.historyMask.Masks(database.HistoryMaskBlocks, blockHash)
}

// EvictDeleted drops the cache entries of the bodies a committed history deletion
// batch deleted and takes them off the count. History deletion calls it without the
// consensus lock, after the commit that carried PutDeletionCountsDirect(len(blockHashes)),
// before the next batch is built: the body cache locks itself, and the exists cache and
// the counts are guarded by bs.lock (block commits update the count by delta under it).
func (bs *blockStore) EvictDeleted(blockHashes []*externalapi.DomainHash) {
	for _, blockHash := range blockHashes {
		bs.cache.Remove(blockHash)
	}
	bs.lock.Lock()
	defer bs.lock.Unlock()
	for _, blockHash := range blockHashes {
		bs.existsCache.Remove(blockHash)
	}
	bs.countCached -= uint64(len(blockHashes))
	bs.deletedCount += uint64(len(blockHashes))
}

// SetHistoryMask installs the mask of the running history deletion job, shared by the
// stores a deletion touches. Call once, before any reader runs.
func (bs *blockStore) SetHistoryMask(mask *database.HistoryMask) {
	bs.historyMask = mask
}
