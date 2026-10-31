package blockstatusstore

import (
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/database"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
)

// StageStatusDirect writes blockHash's status into dbTx exactly as a staging shard
// commit does, without a staging area and without touching the cache. The caller
// commits dbTx and then calls EvictDeleted.
func (bss *blockStatusStore) StageStatusDirect(dbTx model.DBWriter, blockHash *externalapi.DomainHash,
	blockStatus externalapi.BlockStatus) error {
	blockStatusBytes, err := bss.serializeBlockStatus(blockStatus)
	if err != nil {
		return err
	}
	return dbTx.Put(bss.hashAsKey(blockHash), blockStatusBytes)
}

// EvictDeleted drops the cached statuses of blocks whose status a committed history
// deletion batch rewrote. The cache is concurrency-safe; history deletion calls this without
// the consensus lock, after the batch's commit.
func (bss *blockStatusStore) EvictDeleted(blockHashes []*externalapi.DomainHash) {
	for _, blockHash := range blockHashes {
		bss.cache.Remove(blockHash)
	}
}

// SetHistoryMask installs the mask of the running history deletion job, shared by the
// stores a deletion touches. Call once, before any reader runs.
func (bss *blockStatusStore) SetHistoryMask(mask *database.HistoryMask) {
	bss.historyMask = mask
}
