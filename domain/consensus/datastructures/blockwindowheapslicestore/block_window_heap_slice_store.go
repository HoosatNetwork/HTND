package blockwindowheapslicestore

import (
	"github.com/Hoosat-Oy/HTND/domain/consensus/model"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/lrucachehashandwindowsizetoblockghostdagdatahashpairs"
	"github.com/Hoosat-Oy/HTND/infrastructure/db/database"
	"github.com/Hoosat-Oy/HTND/util/staging"
	"github.com/pkg/errors"
)

type blockWindowHeapSliceStore struct {
	shardID model.StagingShardID
	cache   *lrucachehashandwindowsizetoblockghostdagdatahashpairs.LRUCache
}

// New instantiates a new WindowHeapSliceStore
func New(cacheSize int, preallocate bool) model.WindowHeapSliceStore {
	return &blockWindowHeapSliceStore{
		shardID: staging.GenerateShardingID(),
		cache:   lrucachehashandwindowsizetoblockghostdagdatahashpairs.New(cacheSize, preallocate),
	}
}

// Stage stages the given blockStatus for the given blockHash
func (bss *blockWindowHeapSliceStore) Stage(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash, windowSize int, heapSlice []*externalapi.BlockGHOSTDAGDataHashPair) {
	stagingShard := bss.stagingShard(stagingArea)
	stagingShard.toAdd[newShardKey(blockHash, windowSize)] = heapSlice
}

func (bss *blockWindowHeapSliceStore) IsStaged(stagingArea *model.StagingArea) bool {
	return bss.stagingShard(stagingArea).isStaged()
}

func (bss *blockWindowHeapSliceStore) Get(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash, windowSize int) ([]*externalapi.BlockGHOSTDAGDataHashPair, error) {
	stagingShard := bss.stagingShard(stagingArea)

	heapSlice, ok := stagingShard.toAdd[newShardKey(blockHash, windowSize)]

	if ok && heapSlice != nil {
		return heapSlice, nil
	}

	heapSliceCached, ok := bss.cache.Get(blockHash, windowSize)
	if ok && heapSliceCached != nil {
		return heapSliceCached, nil
	}

	return nil, errors.Wrap(database.ErrNotFound, "Window heap slice not found")
}
