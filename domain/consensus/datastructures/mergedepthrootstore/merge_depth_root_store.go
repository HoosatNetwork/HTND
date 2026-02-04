package mergedepthrootstore

import (
	"github.com/Hoosat-Oy/HTND/domain/consensus/model"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/lrucache"
	"github.com/Hoosat-Oy/HTND/util/staging"
)

var bucketName = []byte("merge-depth-roots")

type mergeDepthRootStore struct {
	shardID model.StagingShardID
	cache   *lrucache.LRUCache[*externalapi.DomainHash]
	bucket  model.DBBucket
}

// New instantiates a new MergeDepthRootStore
func New(prefixBucket model.DBBucket, cacheSize int, preallocate bool) model.MergeDepthRootStore {
	return &mergeDepthRootStore{
		shardID: staging.GenerateShardingID(),
		cache:   lrucache.New[*externalapi.DomainHash](cacheSize, preallocate),
		bucket:  prefixBucket.Bucket(bucketName),
	}
}

func (mdrs *mergeDepthRootStore) StageMergeDepthRoot(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash, root *externalapi.DomainHash) {
	stagingShard := mdrs.stagingShard(stagingArea)

	stagingShard.toAdd[*blockHash] = root
}

func (mdrs *mergeDepthRootStore) MergeDepthRoot(dbContext model.DBReader, stagingArea *model.StagingArea, blockHash *externalapi.DomainHash) (*externalapi.DomainHash, error) {
	stagingShard := mdrs.stagingShard(stagingArea)
	root, ok := stagingShard.toAdd[*blockHash]
	if ok && root != nil {
		return root, nil
	}

	rootCached, ok := mdrs.cache.Get(blockHash)
	if ok && rootCached != nil {
		return rootCached, nil
	}

	rootBytes, err := dbContext.Get(mdrs.hashAsKey(blockHash))
	if err != nil {
		return nil, err
	}
	rootDeserialzied, err := externalapi.NewDomainHashFromByteSlice(rootBytes)
	if err != nil {
		return nil, err
	}

	mdrs.cache.Add(blockHash, rootDeserialzied)
	return rootDeserialzied, nil
}

func (mdrs *mergeDepthRootStore) IsStaged(stagingArea *model.StagingArea) bool {
	return mdrs.stagingShard(stagingArea).isStaged()
}

func (mdrs *mergeDepthRootStore) hashAsKey(hash *externalapi.DomainHash) model.DBKey {
	return mdrs.bucket.Key(hash.ByteSlice())
}
