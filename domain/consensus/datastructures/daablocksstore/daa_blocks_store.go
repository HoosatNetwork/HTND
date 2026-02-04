package daablocksstore

import (
	"github.com/Hoosat-Oy/HTND/domain/consensus/database/binaryserialization"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/lrucache"
	"github.com/Hoosat-Oy/HTND/util/staging"
)

var daaScoreBucketName = []byte("daa-score")
var daaAddedBlocksBucketName = []byte("daa-added-blocks")

// daaBlocksStore represents a store of DAABlocksStore
type daaBlocksStore struct {
	shardID                model.StagingShardID
	daaScoreLRUCache       *lrucache.LRUCache[uint64]
	daaAddedBlocksLRUCache *lrucache.LRUCache[[]*externalapi.DomainHash]
	daaScoreBucket         model.DBBucket
	daaAddedBlocksBucket   model.DBBucket
}

// New instantiates a new DAABlocksStore
func New(prefixBucket model.DBBucket, daaScoreCacheSize int, daaAddedBlocksCacheSize int, preallocate bool) model.DAABlocksStore {
	return &daaBlocksStore{
		shardID:                staging.GenerateShardingID(),
		daaScoreLRUCache:       lrucache.New[uint64](daaScoreCacheSize, preallocate),
		daaAddedBlocksLRUCache: lrucache.New[[]*externalapi.DomainHash](daaAddedBlocksCacheSize, preallocate),
		daaScoreBucket:         prefixBucket.Bucket(daaScoreBucketName),
		daaAddedBlocksBucket:   prefixBucket.Bucket(daaAddedBlocksBucketName),
	}
}

func (daas *daaBlocksStore) StageDAAScore(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash, daaScore uint64) {
	stagingShard := daas.stagingShard(stagingArea)

	stagingShard.daaScoreToAdd[*blockHash] = daaScore
}

func (daas *daaBlocksStore) StageBlockDAAAddedBlocks(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash, addedBlocks []*externalapi.DomainHash) {
	stagingShard := daas.stagingShard(stagingArea)

	stagingShard.daaAddedBlocksToAdd[*blockHash] = externalapi.CloneHashes(addedBlocks)
	daas.daaAddedBlocksLRUCache.Add(blockHash, externalapi.CloneHashes(addedBlocks))
}

func (daas *daaBlocksStore) IsStaged(stagingArea *model.StagingArea) bool {
	return daas.stagingShard(stagingArea).isStaged()
}

func (daas *daaBlocksStore) DAAScore(dbContext model.DBReader, stagingArea *model.StagingArea, blockHash *externalapi.DomainHash) (uint64, error) {
	stagingShard := daas.stagingShard(stagingArea)
	if daaScore, ok := stagingShard.daaScoreToAdd[*blockHash]; ok {
		return daaScore, nil
	}

	if daaScore, ok := daas.daaScoreLRUCache.Get(blockHash); ok {
		return daaScore, nil
	}

	daaScoreBytes, err := dbContext.Get(daas.daaScoreHashAsKey(blockHash))
	if err != nil {
		return 0, err
	}

	daaScoreDeserialized, err := binaryserialization.DeserializeUint64(daaScoreBytes)
	if err != nil {
		return 0, err
	}
	daas.daaScoreLRUCache.Add(blockHash, daaScoreDeserialized)
	return daaScoreDeserialized, nil
}

func (daas *daaBlocksStore) DAAAddedBlocks(dbContext model.DBReader, stagingArea *model.StagingArea, blockHash *externalapi.DomainHash) ([]*externalapi.DomainHash, error) {
	stagingShard := daas.stagingShard(stagingArea)
	addedBlocks, ok := stagingShard.daaAddedBlocksToAdd[*blockHash]
	if ok && addedBlocks != nil {
		return externalapi.CloneHashes(addedBlocks), nil
	}
	addedBlocksCached, ok := daas.daaAddedBlocksLRUCache.Get(blockHash)
	if ok && addedBlocksCached != nil {
		return externalapi.CloneHashes(addedBlocksCached), nil
	}

	addedBlocksBytes, err := dbContext.Get(daas.daaAddedBlocksHashAsKey(blockHash))
	if err != nil {
		return nil, err
	}

	addedBlocksDeserialized, err := binaryserialization.DeserializeHashes(addedBlocksBytes)
	if err != nil {
		return nil, err
	}
	daas.daaAddedBlocksLRUCache.Add(blockHash, addedBlocksDeserialized)
	return externalapi.CloneHashes(addedBlocksDeserialized), nil
}

func (daas *daaBlocksStore) daaScoreHashAsKey(hash *externalapi.DomainHash) model.DBKey {
	return daas.daaScoreBucket.Key(hash.ByteSlice())
}

func (daas *daaBlocksStore) daaAddedBlocksHashAsKey(hash *externalapi.DomainHash) model.DBKey {
	return daas.daaAddedBlocksBucket.Key(hash.ByteSlice())
}

func (daas *daaBlocksStore) Delete(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash) {
	stagingShard := daas.stagingShard(stagingArea)
	daas.daaScoreLRUCache.Remove(blockHash)
	daas.daaAddedBlocksLRUCache.Remove(blockHash)

	if _, ok := stagingShard.daaScoreToAdd[*blockHash]; ok {
		delete(stagingShard.daaScoreToAdd, *blockHash)
	} else {
		stagingShard.daaAddedBlocksToDelete[*blockHash] = struct{}{}
	}

	if _, ok := stagingShard.daaAddedBlocksToAdd[*blockHash]; ok {
		delete(stagingShard.daaAddedBlocksToAdd, *blockHash)
	} else {
		stagingShard.daaAddedBlocksToDelete[*blockHash] = struct{}{}
	}
}
