package daawindowstore

import (
	"encoding/binary"

	"github.com/Hoosat-Oy/HTND/domain/consensus/database/serialization"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/lrucachehashpairtoblockghostdagdatahashpair"
	"github.com/Hoosat-Oy/HTND/util/staging"
	"google.golang.org/protobuf/proto"
)

var bucketName = []byte("daa-window")

type daaWindowStore struct {
	shardID model.StagingShardID
	cache   *lrucachehashpairtoblockghostdagdatahashpair.LRUCache
	bucket  model.DBBucket
}

// New instantiates a new BlocksWithTrustedDataDAAWindowStore
func New(prefixBucket model.DBBucket, cacheSize int, preallocate bool) model.BlocksWithTrustedDataDAAWindowStore {
	return &daaWindowStore{
		shardID: staging.GenerateShardingID(),
		cache:   lrucachehashpairtoblockghostdagdatahashpair.New(cacheSize, preallocate),
		bucket:  prefixBucket.Bucket(bucketName),
	}
}

func (daaws *daaWindowStore) Stage(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash, index uint64, pair *externalapi.BlockGHOSTDAGDataHashPair) {
	stagingShard := daaws.stagingShard(stagingArea)

	key := newDBKey(blockHash, index)
	if _, ok := stagingShard.toAdd[key]; !ok {
		stagingShard.toAdd[key] = pair
	}

}

func (daaws *daaWindowStore) DAAWindowBlock(dbContext model.DBReader, stagingArea *model.StagingArea, blockHash *externalapi.DomainHash, index uint64) (*externalapi.BlockGHOSTDAGDataHashPair, error) {
	stagingShard := daaws.stagingShard(stagingArea)

	dbKey := newDBKey(blockHash, index)
	pair, ok := stagingShard.toAdd[dbKey]
	if ok && pair != nil {
		return pair, nil
	}
	pairCached, ok := daaws.cache.Get(blockHash, index)
	if ok && pairCached != nil {
		return pairCached, nil
	}

	pairBytes, err := dbContext.Get(daaws.key(dbKey))
	if err != nil {
		return nil, err
	}

	pairDeserialized, err := deserializePairBytes(pairBytes)
	if err != nil {
		return nil, err
	}

	daaws.cache.Add(blockHash, index, pairDeserialized)
	return pairDeserialized, nil
}

func deserializePairBytes(pairBytes []byte) (*externalapi.BlockGHOSTDAGDataHashPair, error) {
	dbPair := &serialization.DbBlockGHOSTDAGDataHashPair{}
	err := proto.Unmarshal(pairBytes, dbPair)
	if err != nil {
		return nil, err
	}

	return serialization.DbBlockGHOSTDAGDataHashPairToBlockGHOSTDAGDataHashPair(dbPair)
}

func (daaws *daaWindowStore) IsStaged(stagingArea *model.StagingArea) bool {
	return daaws.stagingShard(stagingArea).isStaged()
}

func (daaws *daaWindowStore) key(key dbKey) model.DBKey {
	keyIndexBytes := make([]byte, 8)
	binary.LittleEndian.PutUint64(keyIndexBytes, key.index)
	return daaws.bucket.Bucket(key.blockHash.ByteSlice()).Key(keyIndexBytes)
}
