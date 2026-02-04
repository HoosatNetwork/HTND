package finalitystore

import (
	"github.com/Hoosat-Oy/HTND/domain/consensus/model"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/lrucache"
	"github.com/Hoosat-Oy/HTND/util/staging"
)

var bucketName = []byte("finality-points")

type finalityStore struct {
	shardID model.StagingShardID
	cache   *lrucache.LRUCache[*externalapi.DomainHash]
	bucket  model.DBBucket
}

// New instantiates a new FinalityStore
func New(prefixBucket model.DBBucket, cacheSize int, preallocate bool) model.FinalityStore {
	return &finalityStore{
		shardID: staging.GenerateShardingID(),
		cache:   lrucache.New[*externalapi.DomainHash](cacheSize, preallocate),
		bucket:  prefixBucket.Bucket(bucketName),
	}
}

func (fs *finalityStore) StageFinalityPoint(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash, finalityPointHash *externalapi.DomainHash) {
	stagingShard := fs.stagingShard(stagingArea)

	stagingShard.toAdd[*blockHash] = finalityPointHash
}

func (fs *finalityStore) FinalityPoint(dbContext model.DBReader, stagingArea *model.StagingArea, blockHash *externalapi.DomainHash) (*externalapi.DomainHash, error) {
	stagingShard := fs.stagingShard(stagingArea)
	finalityPointHash, ok := stagingShard.toAdd[*blockHash]
	if ok && finalityPointHash != nil {
		return finalityPointHash, nil
	}

	finalityPointHashCached, ok := fs.cache.Get(blockHash)
	if ok && finalityPointHashCached != nil {
		return finalityPointHashCached, nil
	}

	finalityPointHashBytes, err := dbContext.Get(fs.hashAsKey(blockHash))
	if err != nil {
		return nil, err
	}
	finalityPointHashDeserialized, err := externalapi.NewDomainHashFromByteSlice(finalityPointHashBytes)
	if err != nil {
		return nil, err
	}

	fs.cache.Add(blockHash, finalityPointHashDeserialized)
	return finalityPointHashDeserialized, nil
}

func (fs *finalityStore) IsStaged(stagingArea *model.StagingArea) bool {
	return fs.stagingShard(stagingArea).isStaged()
}

func (fs *finalityStore) hashAsKey(hash *externalapi.DomainHash) model.DBKey {
	return fs.bucket.Key(hash.ByteSlice())
}
