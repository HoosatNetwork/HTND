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

func (bss *blockStagingShard) commitCount(dbTx model.DBTransaction) error {
	count := bss.store.count(bss)
	countBytes, err := bss.store.serializeBlockCount(count)
	if err != nil {
		return err
	}
	err = dbTx.Put(bss.store.countKey, countBytes)
	if err != nil {
		return err
	}
	bss.store.countCached = count
	return nil
}

func (bss *blockStagingShard) isStaged() bool {
	return len(bss.toAdd) != 0 || len(bss.toDelete) != 0
}
