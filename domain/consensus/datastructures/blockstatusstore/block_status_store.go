package blockstatusstore

import (
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/database"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/database/serialization"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/lrucache"
	"github.com/HoosatNetwork/HTND/v2/util/staging"
	"github.com/cockroachdb/errors"
)

var bucketName = []byte("block-statuses")

// blockStatusStore represents a store of BlockStatuses
type blockStatusStore struct {
	shardID model.StagingShardID
	cache   *lrucache.ConcurrentLRUCache[externalapi.BlockStatus]
	bucket  model.DBBucket
	// historyMask makes the blocks a running history deletion job deletes read as
	// header-only (see database.HistoryMask). Set once, before any reader runs.
	historyMask *database.HistoryMask
}

// New instantiates a new BlockStatusStore
func New(prefixBucket model.DBBucket, cacheSize int, preallocate bool) model.BlockStatusStore {
	return &blockStatusStore{
		shardID: staging.GenerateShardingID(),
		cache:   lrucache.NewConcurrent[externalapi.BlockStatus](cacheSize, preallocate),
		bucket:  prefixBucket.Bucket(bucketName),
	}
}

// Stage stages the given blockStatus for the given blockHash
func (bss *blockStatusStore) Stage(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash, blockStatus externalapi.BlockStatus) {
	stagingShard := bss.stagingShard(stagingArea)
	stagingShard.toAdd[*blockHash] = blockStatus.Clone()
}

func (bss *blockStatusStore) IsStaged(stagingArea *model.StagingArea) bool {
	return bss.stagingShard(stagingArea).isStaged()
}

// Get gets the blockStatus associated with the given blockHash
func (bss *blockStatusStore) Get(dbContext model.DBReader, stagingArea *model.StagingArea, blockHash *externalapi.DomainHash) (externalapi.BlockStatus, error) {
	stagingShard := bss.stagingShard(stagingArea)

	if status, ok := stagingShard.toAdd[*blockHash]; ok {
		return status, nil
	}

	if bss.historyMask.Masks(database.HistoryMaskStatus, blockHash) {
		return externalapi.StatusHeaderOnly, nil
	}

	if status, ok := bss.cache.Get(blockHash); ok {
		return status, nil
	}

	statusBytes, err := dbContext.Get(bss.hashAsKey(blockHash))
	if errors.Is(err, database.ErrNotFound) {
		return 0, errors.Wrapf(err, "Block status %s does not exist in db", blockHash)
	}
	if err != nil {
		return 0, err
	}

	statusDeserialized, err := bss.deserializeBlockStatus(statusBytes)
	if err != nil {
		return 0, err
	}
	bss.cache.Add(blockHash, statusDeserialized)
	return statusDeserialized, nil
}

// GetWithoutCaching reads a block's last committed status from the database alone, reporting
// whether it exists, for callers that do not hold the consensus lock. Block processing writes the
// status cache under the lock, so a lock-free reader must not touch it. A status changes as a block is
// verified; this returns the one in the last committed transaction.
func (bss *blockStatusStore) GetWithoutCaching(dbContext model.DBReader, blockHash *externalapi.DomainHash) (
	status externalapi.BlockStatus, exists bool, err error,
) {
	if bss.historyMask.Masks(database.HistoryMaskStatus, blockHash) {
		return externalapi.StatusHeaderOnly, true, nil
	}
	statusBytes, err := dbContext.Get(bss.hashAsKey(blockHash))
	if errors.Is(err, database.ErrNotFound) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	status, err = bss.deserializeBlockStatus(statusBytes)
	if err != nil {
		return 0, false, err
	}
	return status, true, nil
}

// Exists returns true if the blockStatus for the given blockHash exists
func (bss *blockStatusStore) Exists(dbContext model.DBReader, stagingArea *model.StagingArea, blockHash *externalapi.DomainHash) (bool, error) {
	stagingShard := bss.stagingShard(stagingArea)

	if _, ok := stagingShard.toAdd[*blockHash]; ok {
		return true, nil
	}

	if bss.historyMask.Masks(database.HistoryMaskStatus, blockHash) {
		return true, nil
	}

	if bss.cache.Has(blockHash) {
		return true, nil
	}

	statusBytes, err := dbContext.Get(bss.hashAsKey(blockHash))
	if err != nil || statusBytes == nil {
		return false, nil
	}

	statusDeserialized, err := bss.deserializeBlockStatus(statusBytes)
	if err != nil {
		return false, err
	}
	bss.cache.Add(blockHash, statusDeserialized)
	return true, nil
}

func (bss *blockStatusStore) serializeBlockStatus(status externalapi.BlockStatus) ([]byte, error) {
	dbBlockStatus := serialization.DomainBlockStatusToDbBlockStatus(status)
	return dbBlockStatus.MarshalVT()
}

func (bss *blockStatusStore) deserializeBlockStatus(statusBytes []byte) (externalapi.BlockStatus, error) {
	dbBlockStatus := &serialization.DbBlockStatus{}
	err := dbBlockStatus.UnmarshalVT(statusBytes)
	if err != nil {
		return 0, err
	}
	return serialization.DbBlockStatusToDomainBlockStatus(dbBlockStatus), nil
}

func (bss *blockStatusStore) hashAsKey(hash *externalapi.DomainHash) model.DBKey {
	return bss.bucket.Key(hash.ByteSlice())
}

func (bss *blockStatusStore) UnstageAll(stagingArea *model.StagingArea) {
	stagingShard := bss.stagingShard(stagingArea)
	stagingShard.toAdd = make(map[externalapi.DomainHash]externalapi.BlockStatus)
}

func (bss *blockStatusStore) CacheLen() int {
	return bss.cache.Len()
}

func (bss *blockStatusStore) ClearCache() {
	bss.cache.Clear()
}
