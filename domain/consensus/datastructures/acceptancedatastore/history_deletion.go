package acceptancedatastore

import (
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/database"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
)

// DeleteBlockDataDirect writes into dbTx the delete a staging shard commit writes for
// a deleted block, without a staging area and without touching the cache.
func (ads *acceptanceDataStore) DeleteBlockDataDirect(dbTx model.DBWriter, blockHash *externalapi.DomainHash) error {
	return dbTx.Delete(ads.hashAsKey(blockHash))
}

// EvictDeleted drops the cache entries of blocks a committed history deletion batch
// deleted. The cache is concurrency-safe; history deletion calls this without the
// consensus lock, after the batch's commit.
func (ads *acceptanceDataStore) EvictDeleted(blockHashes []*externalapi.DomainHash) {
	for _, blockHash := range blockHashes {
		ads.cache.Remove(blockHash)
	}
}

// SetHistoryMask installs the mask of the running history deletion job, shared by the
// stores a deletion touches. Call once, before any reader runs.
func (ads *acceptanceDataStore) SetHistoryMask(mask *database.HistoryMask) {
	ads.historyMask = mask
}
