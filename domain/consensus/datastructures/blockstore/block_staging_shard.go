package blockstore

import (
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/util/memory"
)

type blockStagingShard struct {
	store    *blockStore
	toAdd    map[externalapi.DomainHash]*externalapi.DomainBlock
	toDelete map[externalapi.DomainHash]struct{}
}

func (bs *blockStore) stagingShard(stagingArea *model.StagingArea) *blockStagingShard {
	return stagingArea.GetOrCreateShard(bs.shardID, func() model.StagingShard {
		return &blockStagingShard{
			store:    bs,
			toAdd:    make(map[externalapi.DomainHash]*externalapi.DomainBlock),
			toDelete: make(map[externalapi.DomainHash]struct{}),
		}
	}).(*blockStagingShard)
}

func (bss *blockStagingShard) Commit(dbTx model.DBTransaction) error {
	for hash, block := range bss.toAdd {
		buffer, err := bss.store.serializeBlockOffHeap(block)
		if err != nil {
			return err
		}
		err = dbTx.Put(bss.store.hashAsKey(&hash), buffer.Slice())
		if err != nil {
			memory.Free(buffer)
			return err
		}
		// The database copied the bytes, so the cache can keep the buffer itself.
		bss.store.cache.AddBuffer(&hash, buffer, block.PoWHash)
	}

	for hash := range bss.toDelete {
		err := dbTx.Delete(bss.store.hashAsKey(&hash))
		if err != nil {
			return err
		}
		bss.store.cache.Remove(&hash)
		bss.store.lock.Lock()
		bss.store.existsCache.Remove(&hash)
		bss.store.lock.Unlock()
	}

	err := bss.commitCount(dbTx)
	if err != nil {
		return err
	}

	return nil
}

// commitCount writes blocks-count and the deletions it reflects (blocks-count-deleted). History
// deletion batches take their bodies off countCached and add them to deletedCount without the
// consensus lock (EvictDeleted), so the count is updated by delta and read together with the
// deletions in one section under bs.lock.
func (bss *blockStagingShard) commitCount(dbTx model.DBTransaction) error {
	bss.store.lock.Lock()
	delta := uint64(len(bss.toAdd)) - uint64(len(bss.toDelete))
	count, deleted := bss.store.countCached+delta, bss.store.deletedCount
	bss.store.lock.Unlock()
	countBytes, err := bss.store.serializeBlockCount(count)
	if err != nil {
		return err
	}
	err = dbTx.Put(bss.store.countKey, countBytes)
	if err != nil {
		return err
	}
	// With no history deletions, preserve the legacy count schema. A zero
	// counter need not be recorded: a missing split key already reads as zero.
	if deleted != 0 {
		deletedBytes, err := bss.store.serializeBlockCount(deleted)
		if err != nil {
			return err
		}
		if err = dbTx.Put(bss.store.countedKey, deletedBytes); err != nil {
			return err
		}
	}
	// Publish only after both writes succeed. Apply the delta rather than the
	// captured count because history eviction can decrement it during the writes.
	bss.store.lock.Lock()
	bss.store.countCached += delta
	bss.store.lock.Unlock()
	return nil
}

func (bss *blockStagingShard) isStaged() bool {
	return len(bss.toAdd) != 0 || len(bss.toDelete) != 0
}
