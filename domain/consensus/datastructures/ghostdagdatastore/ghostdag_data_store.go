package ghostdagdatastore

import (
	"github.com/Hoosat-Oy/HTND/domain/consensus/database/serialization"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/lrucacheghostdagdata"
	"github.com/Hoosat-Oy/HTND/util/staging"
	"google.golang.org/protobuf/proto"
)

var ghostdagDataBucketName = []byte("block-ghostdag-data")
var trustedDataBucketName = []byte("block-with-trusted-data-ghostdag-data")

// ghostdagDataStore represents a store of BlockGHOSTDAGData
type ghostdagDataStore struct {
	shardID            model.StagingShardID
	cache              *lrucacheghostdagdata.LRUCache
	ghostdagDataBucket model.DBBucket
	trustedDataBucket  model.DBBucket
}

// New instantiates a new GHOSTDAGDataStore
func New(prefixBucket model.DBBucket, cacheSize int, preallocate bool) model.GHOSTDAGDataStore {
	return &ghostdagDataStore{
		shardID:            staging.GenerateShardingID(),
		cache:              lrucacheghostdagdata.New(cacheSize, preallocate),
		ghostdagDataBucket: prefixBucket.Bucket(ghostdagDataBucketName),
		trustedDataBucket:  prefixBucket.Bucket(trustedDataBucketName),
	}
}

// Stage stages the given blockGHOSTDAGData for the given blockHash
func (gds *ghostdagDataStore) Stage(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash,
	blockGHOSTDAGData *externalapi.BlockGHOSTDAGData, isTrustedData bool) {

	stagingShard := gds.stagingShard(stagingArea)

	stagingShard.toAdd[newKey(blockHash, isTrustedData)] = blockGHOSTDAGData
}

func (gds *ghostdagDataStore) IsStaged(stagingArea *model.StagingArea) bool {
	return gds.stagingShard(stagingArea).isStaged()
}

// Get gets the blockGHOSTDAGData associated with the given blockHash
func (gds *ghostdagDataStore) Get(dbContext model.DBReader, stagingArea *model.StagingArea, blockHash *externalapi.DomainHash, isTrustedData bool) (*externalapi.BlockGHOSTDAGData, error) {
	stagingShard := gds.stagingShard(stagingArea)

	key := newKey(blockHash, isTrustedData)
	blockGHOSTDAGData, ok := stagingShard.toAdd[key]
	if ok && blockGHOSTDAGData != nil {
		return blockGHOSTDAGData, nil
	}

	blockGHOSTDAGDataCached, ok := gds.cache.Get(blockHash, isTrustedData)
	if ok && blockGHOSTDAGDataCached != nil {
		return blockGHOSTDAGDataCached, nil
	}

	blockGHOSTDAGDataBytes, err := dbContext.Get(gds.serializeKey(key))
	if err != nil {
		return nil, err
	}

	blockGHOSTDAGDataDeserialized, err := gds.deserializeBlockGHOSTDAGData(blockGHOSTDAGDataBytes)
	if err != nil {
		return nil, err
	}
	gds.cache.Add(blockHash, isTrustedData, blockGHOSTDAGDataDeserialized)
	return blockGHOSTDAGDataDeserialized, nil
}

func (gds *ghostdagDataStore) UnstageAll(stagingArea *model.StagingArea) {
	stagingShard := gds.stagingShard(stagingArea)
	gds.cache.Clear()
	stagingShard.toAdd = make(map[key]*externalapi.BlockGHOSTDAGData)
}

func (gds *ghostdagDataStore) serializeKey(k key) model.DBKey {
	if k.isTrustedData {
		return gds.trustedDataBucket.Key(k.hash.ByteSlice())
	}
	return gds.ghostdagDataBucket.Key(k.hash.ByteSlice())
}

func (gds *ghostdagDataStore) serializeBlockGHOSTDAGData(blockGHOSTDAGData *externalapi.BlockGHOSTDAGData) ([]byte, error) {
	return proto.Marshal(serialization.BlockGHOSTDAGDataToDBBlockGHOSTDAGData(blockGHOSTDAGData))
}

func (gds *ghostdagDataStore) deserializeBlockGHOSTDAGData(blockGHOSTDAGDataBytes []byte) (*externalapi.BlockGHOSTDAGData, error) {
	dbBlockGHOSTDAGData := &serialization.DbBlockGhostdagData{}
	err := proto.Unmarshal(blockGHOSTDAGDataBytes, dbBlockGHOSTDAGData)
	if err != nil {
		return nil, err
	}

	return serialization.DBBlockGHOSTDAGDataToBlockGHOSTDAGData(dbBlockGHOSTDAGData)
}
