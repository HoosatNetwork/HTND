package daablocksstore

import (
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/database"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
)

// DeleteBlockDataDirect writes into dbTx what a staging shard commit writes for a
// block Delete staged: the DAA-added-blocks key only. Delete stages the DAA score key
// into daaAddedBlocksToDelete as well, so the score has never been deleted; the direct
// path keeps that end state byte for byte.
func (daas *daaBlocksStore) DeleteBlockDataDirect(dbTx model.DBWriter, blockHash *externalapi.DomainHash) error {
	return dbTx.Delete(daas.daaAddedBlocksHashAsKey(blockHash))
}

// EvictDeleted drops the cache entries Delete drops. The caches are concurrency-safe; history
// deletion calls this without the consensus lock, after the batch's commit.
func (daas *daaBlocksStore) EvictDeleted(blockHashes []*externalapi.DomainHash) {
	for _, blockHash := range blockHashes {
		daas.daaScoreLRUCache.Remove(blockHash)
		daas.daaAddedBlocksLRUCache.Remove(blockHash)
	}
}

// SetHistoryMask installs the mask of the running history deletion job, shared by the
// stores a deletion touches. Call once, before any reader runs.
func (daas *daaBlocksStore) SetHistoryMask(mask *database.HistoryMask) {
	daas.historyMask = mask
}
