package blockrelationstore

import (
	"unsafe"

	"github.com/HoosatNetwork/HTND/domain/consensus/database"
	"github.com/HoosatNetwork/HTND/domain/consensus/database/serialization"
	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/lrucache"
	"github.com/HoosatNetwork/HTND/util/staging"
	"github.com/cockroachdb/errors"
)

var bucketName = []byte("block-relations")

// blockRelationStore represents a store of BlockRelations
type blockRelationStore struct {
	shardID model.StagingShardID
	cache   *lrucache.LRUCache[*model.BlockRelations]
	bucket  model.DBBucket
}

// New instantiates a new BlockRelationStore
func New(prefixBucket model.DBBucket, cacheSize int, preallocate bool) model.BlockRelationStore {
	return &blockRelationStore{
		shardID: staging.GenerateShardingID(),
		cache:   lrucache.New[*model.BlockRelations](cacheSize, preallocate),
		bucket:  prefixBucket.Bucket(bucketName),
	}
}

func (brs *blockRelationStore) StageBlockRelation(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash, blockRelations *model.BlockRelations) {
	stagingShard := brs.stagingShard(stagingArea)

	stagingShard.toAdd[*blockHash] = blockRelations.Clone()
}

func (brs *blockRelationStore) IsStaged(stagingArea *model.StagingArea) bool {
	return brs.stagingShard(stagingArea).isStaged()
}

func (brs *blockRelationStore) BlockRelation(dbContext model.DBReader, stagingArea *model.StagingArea, blockHash *externalapi.DomainHash) (*model.BlockRelations, error) {
	stagingShard := brs.stagingShard(stagingArea)
	blockRelations, ok := stagingShard.toAdd[*blockHash]
	if ok && blockRelations != nil {
		return blockRelations.Clone(), nil
	}

	blockRelationsCached, ok := brs.cache.Get(blockHash)
	if ok && blockRelationsCached != nil {
		return blockRelationsCached.Clone(), nil
	}

	blockRelationsBytes, err := dbContext.Get(brs.hashAsKey(blockHash))
	if errors.Is(err, database.ErrNotFound) {
		return nil, errors.Wrapf(err, "Block relation %s does not exist in db", blockHash)
	}
	if err != nil {
		return nil, err
	}

	blockRelationsDeserialized, err := brs.deserializeBlockRelations(blockRelationsBytes)
	if err != nil {
		return nil, err
	}
	brs.cache.Add(blockHash, blockRelationsDeserialized)
	return blockRelationsDeserialized.Clone(), nil
}

func (brs *blockRelationStore) Has(dbContext model.DBReader, stagingArea *model.StagingArea, blockHash *externalapi.DomainHash) (bool, error) {
	stagingShard := brs.stagingShard(stagingArea)

	if _, ok := stagingShard.toAdd[*blockHash]; ok {
		return true, nil
	}

	if brs.cache.Has(blockHash) {
		return true, nil
	}

	return dbContext.Has(brs.hashAsKey(blockHash))
}

func (brs *blockRelationStore) UnstageAll(stagingArea *model.StagingArea) {
	stagingShard := brs.stagingShard(stagingArea)
	brs.cache.Clear()
	clear(stagingShard.toAdd)
}

func (brs *blockRelationStore) hashAsKey(hash *externalapi.DomainHash) model.DBKey {
	// Reinterpret the pointer to DomainHash directly as a byte slice.
	// This accesses the underlying memory without calling ByteArray() or making a copy.
	hashBytes := unsafe.Slice((*byte)(unsafe.Pointer(hash)), constants.DomainHashSize)
	return brs.bucket.Key(hashBytes)
}

func (brs *blockRelationStore) serializeBlockRelations(blockRelations *model.BlockRelations) ([]byte, error) {
	dbBlockRelations := serialization.DomainBlockRelationsToDbBlockRelations(blockRelations)
	return dbBlockRelations.MarshalVT()
}

func (brs *blockRelationStore) deserializeBlockRelations(blockRelationsBytes []byte) (*model.BlockRelations, error) {
	dbBlockRelations := &serialization.DbBlockRelations{}
	err := dbBlockRelations.UnmarshalVTUnsafe(blockRelationsBytes)
	if err != nil {
		return nil, err
	}
	return serialization.DbBlockRelationsToDomainBlockRelations(dbBlockRelations)
}

func (brs *blockRelationStore) CacheLen() int {
	return brs.cache.Len()
}
