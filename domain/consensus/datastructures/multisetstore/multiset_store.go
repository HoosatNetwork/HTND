package multisetstore

import (
	"github.com/Hoosat-Oy/HTND/domain/consensus/database/serialization"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/lrucache"
	"github.com/Hoosat-Oy/HTND/util/staging"
	"google.golang.org/protobuf/proto"
)

var bucketName = []byte("multisets")

// multisetStore represents a store of Multisets
type multisetStore struct {
	shardID model.StagingShardID
	cache   *lrucache.LRUCache[model.Multiset]
	bucket  model.DBBucket
}

// New instantiates a new MultisetStore
func New(prefixBucket model.DBBucket, cacheSize int, preallocate bool) model.MultisetStore {
	return &multisetStore{
		shardID: staging.GenerateShardingID(),
		cache:   lrucache.New[model.Multiset](cacheSize, preallocate),
		bucket:  prefixBucket.Bucket(bucketName),
	}
}

// Stage stages the given multiset for the given blockHash
func (ms *multisetStore) Stage(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash, multiset model.Multiset) {
	stagingShard := ms.stagingShard(stagingArea)

	stagingShard.toAdd[*blockHash] = multiset.Clone()
}

func (ms *multisetStore) IsStaged(stagingArea *model.StagingArea) bool {
	return ms.stagingShard(stagingArea).isStaged()
}

// Get gets the multiset associated with the given blockHash
func (ms *multisetStore) Get(dbContext model.DBReader, stagingArea *model.StagingArea, blockHash *externalapi.DomainHash) (model.Multiset, error) {
	stagingShard := ms.stagingShard(stagingArea)
	multiset, ok := stagingShard.toAdd[*blockHash]
	if ok && multiset != nil {
		return multiset.Clone(), nil
	}
	multisetCached, ok := ms.cache.Get(blockHash)
	if ok && multisetCached != nil {
		return multisetCached.Clone(), nil
	}

	multisetBytes, err := dbContext.Get(ms.hashAsKey(blockHash))
	if err != nil {
		return nil, err
	}

	multisetDeserialized, err := ms.deserializeMultiset(multisetBytes)
	if err != nil {
		return nil, err
	}
	ms.cache.Add(blockHash, multisetDeserialized)
	return multisetDeserialized.Clone(), nil
}

// Delete deletes the multiset associated with the given blockHash
func (ms *multisetStore) Delete(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash) {
	stagingShard := ms.stagingShard(stagingArea)
	ms.cache.Remove(blockHash)

	if _, ok := stagingShard.toAdd[*blockHash]; ok {
		delete(stagingShard.toAdd, *blockHash)
		return
	}
	stagingShard.toDelete[*blockHash] = struct{}{}
}

func (ms *multisetStore) hashAsKey(hash *externalapi.DomainHash) model.DBKey {
	return ms.bucket.Key(hash.ByteSlice())
}

func (ms *multisetStore) serializeMultiset(multiset model.Multiset) ([]byte, error) {
	return proto.Marshal(serialization.MultisetToDBMultiset(multiset))
}

func (ms *multisetStore) deserializeMultiset(multisetBytes []byte) (model.Multiset, error) {
	dbMultiset := &serialization.DbMultiset{}
	err := proto.Unmarshal(multisetBytes, dbMultiset)
	if err != nil {
		return nil, err
	}

	return serialization.DBMultisetToMultiset(dbMultiset)
}
