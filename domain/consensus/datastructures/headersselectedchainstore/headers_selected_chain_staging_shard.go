package headersselectedchainstore

import (
	"fmt"

	"github.com/Hoosat-Oy/HTND/domain/consensus/database/binaryserialization"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
)

type headersSelectedChainStagingShard struct {
	store          *headersSelectedChainStore
	addedByHash    map[externalapi.DomainHash]uint64
	removedByHash  map[externalapi.DomainHash]struct{}
	addedByIndex   map[uint64]*externalapi.DomainHash
	removedByIndex map[uint64]struct{}
}

func (hscs *headersSelectedChainStore) stagingShard(stagingArea *model.StagingArea) *headersSelectedChainStagingShard {
	return stagingArea.GetOrCreateShard(hscs.shardID, func() model.StagingShard {
		return &headersSelectedChainStagingShard{
			store:          hscs,
			addedByHash:    make(map[externalapi.DomainHash]uint64),
			removedByHash:  make(map[externalapi.DomainHash]struct{}),
			addedByIndex:   make(map[uint64]*externalapi.DomainHash),
			removedByIndex: make(map[uint64]struct{}),
		}
	}).(*headersSelectedChainStagingShard)
}

func (hscss *headersSelectedChainStagingShard) Commit(dbTx model.DBTransaction) error {
	if !hscss.isStaged() {
		return nil
	}

	prevHighest, prevExists, err := hscss.store.highestChainBlockIndex(dbTx)
	if err != nil {
		return err
	}
	prevLength := uint64(0)
	if prevExists {
		prevLength = prevHighest + 1
	}
	removedCount := uint64(len(hscss.removedByIndex))
	addedCount := uint64(len(hscss.addedByIndex))
	if prevLength < removedCount {
		return fmt.Errorf("headersSelectedChainStagingShard: removed %d entries from chain of length %d", removedCount, prevLength)
	}
	newLength := prevLength - removedCount + addedCount

	for hash := range hscss.removedByHash {
		hashCopy := hash
		err := dbTx.Delete(hscss.store.hashAsKey(&hashCopy))
		if err != nil {
			return err
		}
		hscss.store.cacheByHash.Remove(&hashCopy)
	}

	for index := range hscss.removedByIndex {
		err := dbTx.Delete(hscss.store.indexAsKey(index))
		if err != nil {
			return err
		}
		hscss.store.cacheByIndex.Remove(index)
	}

	for hash, index := range hscss.addedByHash {
		hashCopy := hash
		err := dbTx.Put(hscss.store.hashAsKey(&hashCopy), hscss.store.serializeIndex(index))
		if err != nil {
			return err
		}

		err = dbTx.Put(hscss.store.indexAsKey(index), binaryserialization.SerializeHash(&hashCopy))
		if err != nil {
			return err
		}

		hscss.store.cacheByHash.Add(&hashCopy, index)
		hscss.store.cacheByIndex.Add(index, &hashCopy)

	}

	if newLength == 0 {
		err := dbTx.Delete(hscss.store.highestChainBlockIndexKey)
		if err != nil {
			return err
		}
		hscss.store.cacheHighestChainBlockIndex = 0
		return nil
	}

	newHighest := newLength - 1
	err = dbTx.Put(hscss.store.highestChainBlockIndexKey, hscss.store.serializeIndex(newHighest))
	if err != nil {
		return err
	}

	hscss.store.cacheHighestChainBlockIndex = newHighest

	return nil
}

func (hscss *headersSelectedChainStagingShard) isStaged() bool {
	return len(hscss.addedByHash) != 0 ||
		len(hscss.removedByHash) != 0 ||
		len(hscss.addedByIndex) != 0 ||
		len(hscss.removedByIndex) != 0
}

func (hscss *headersSelectedChainStagingShard) UnstageAll() {
	hscss.addedByHash = make(map[externalapi.DomainHash]uint64)
	hscss.removedByHash = make(map[externalapi.DomainHash]struct{})
	hscss.addedByIndex = make(map[uint64]*externalapi.DomainHash)
	hscss.removedByIndex = make(map[uint64]struct{})
}
