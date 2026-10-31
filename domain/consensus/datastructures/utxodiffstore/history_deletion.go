package utxodiffstore

import (
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/database"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
)

// DeleteBlockDataDirect writes into dbTx the deletes a staging shard commit writes for
// a deleted block (diff and diff child), without a staging area or the caches.
func (uds *utxoDiffStore) DeleteBlockDataDirect(dbTx model.DBWriter, blockHash *externalapi.DomainHash) error {
	if err := dbTx.Delete(uds.utxoDiffHashAsKey(blockHash)); err != nil {
		return err
	}
	return dbTx.Delete(uds.utxoDiffChildHashAsKey(blockHash))
}

// EvictDeleted drops the cache entries of blocks a committed history deletion batch
// deleted. The cache is concurrency-safe; history deletion calls this without the
// consensus lock, after the batch's commit.
func (uds *utxoDiffStore) EvictDeleted(blockHashes []*externalapi.DomainHash) {
	for _, blockHash := range blockHashes {
		uds.utxoDiffCache.Remove(blockHash)
		uds.utxoDiffChildCache.Remove(blockHash)
	}
}

// SetHistoryMask installs the mask of the running history deletion job, shared by the
// stores a deletion touches. Call once, before any reader runs.
func (uds *utxoDiffStore) SetHistoryMask(mask *database.HistoryMask) {
	uds.historyMask = mask
}
