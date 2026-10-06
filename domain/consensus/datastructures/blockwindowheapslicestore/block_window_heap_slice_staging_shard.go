package blockwindowheapslicestore

import (
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
)

type shardKey struct {
	hash       externalapi.DomainHash
	windowSize int
	// See the same field on the LRU cache's key: the trusted-window and pruning-boundary windows
	// are different answers for the same block and size, so they need separate entries (HTN-204).
	includeTrustedWindow bool
}

type blockWindowHeapSliceStagingShard struct {
	store *blockWindowHeapSliceStore
	toAdd map[shardKey][]*externalapi.BlockGHOSTDAGDataHashPair
}

func (bss *blockWindowHeapSliceStore) stagingShard(stagingArea *model.StagingArea) *blockWindowHeapSliceStagingShard {
	return stagingArea.GetOrCreateShard(bss.shardID, func() model.StagingShard {
		return &blockWindowHeapSliceStagingShard{
			store: bss,
			toAdd: make(map[shardKey][]*externalapi.BlockGHOSTDAGDataHashPair),
		}
	}).(*blockWindowHeapSliceStagingShard)
}

func (bsss *blockWindowHeapSliceStagingShard) Commit(_ model.DBTransaction) error {
	for key, heapSlice := range bsss.toAdd {
		bsss.store.cache.Add(&key.hash, key.windowSize, key.includeTrustedWindow, heapSlice)
	}

	return nil
}

func (bsss *blockWindowHeapSliceStagingShard) isStaged() bool {
	return len(bsss.toAdd) != 0
}

func (bsss *blockWindowHeapSliceStagingShard) UnstageAll() {
	bsss.toAdd = make(map[shardKey][]*externalapi.BlockGHOSTDAGDataHashPair)
}

func newShardKey(hash *externalapi.DomainHash, windowSize int, includeTrustedWindow bool) shardKey {
	return shardKey{
		hash:                 *hash,
		windowSize:           windowSize,
		includeTrustedWindow: includeTrustedWindow,
	}
}
