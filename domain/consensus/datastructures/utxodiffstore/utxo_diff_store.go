package utxodiffstore

import (
	"github.com/Hoosat-Oy/HTND/domain/consensus/database/serialization"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/lrucache"
	"github.com/Hoosat-Oy/HTND/util/staging"
	"github.com/pkg/errors"
	"google.golang.org/protobuf/proto"
)

var utxoDiffBucketName = []byte("utxo-diffs")
var utxoDiffChildBucketName = []byte("utxo-diff-children")

// utxoDiffStore represents a store of UTXODiffs
type utxoDiffStore struct {
	shardID             model.StagingShardID
	utxoDiffCache       *lrucache.LRUCache[externalapi.UTXODiff]
	utxoDiffChildCache  *lrucache.LRUCache[*externalapi.DomainHash]
	utxoDiffBucket      model.DBBucket
	utxoDiffChildBucket model.DBBucket
}

// New instantiates a new UTXODiffStore
func New(prefixBucket model.DBBucket, cacheSize int, preallocate bool) model.UTXODiffStore {
	return &utxoDiffStore{
		shardID:             staging.GenerateShardingID(),
		utxoDiffCache:       lrucache.New[externalapi.UTXODiff](cacheSize, preallocate),
		utxoDiffChildCache:  lrucache.New[*externalapi.DomainHash](cacheSize, preallocate),
		utxoDiffBucket:      prefixBucket.Bucket(utxoDiffBucketName),
		utxoDiffChildBucket: prefixBucket.Bucket(utxoDiffChildBucketName),
	}
}

// Stage stages the given utxoDiff for the given blockHash
func (uds *utxoDiffStore) Stage(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash,
	utxoDiff externalapi.UTXODiff, utxoDiffChild *externalapi.DomainHash) {

	stagingShard := uds.stagingShard(stagingArea)

	stagingShard.utxoDiffToAdd[*blockHash] = utxoDiff

	if utxoDiffChild != nil {
		stagingShard.utxoDiffChildToAdd[*blockHash] = utxoDiffChild
	}
}

func (uds *utxoDiffStore) IsStaged(stagingArea *model.StagingArea) bool {
	return uds.stagingShard(stagingArea).isStaged()
}

func (uds *utxoDiffStore) isBlockHashStaged(stagingShard *utxoDiffStagingShard, blockHash *externalapi.DomainHash) bool {
	if _, ok := stagingShard.utxoDiffToAdd[*blockHash]; ok {
		return true
	}
	_, ok := stagingShard.utxoDiffChildToAdd[*blockHash]
	return ok
}

// UTXODiff gets the utxoDiff associated with the given blockHash
func (uds *utxoDiffStore) UTXODiff(dbContext model.DBReader, stagingArea *model.StagingArea, blockHash *externalapi.DomainHash) (externalapi.UTXODiff, error) {
	stagingShard := uds.stagingShard(stagingArea)

	utxoDiff, ok := stagingShard.utxoDiffToAdd[*blockHash]
	if ok && utxoDiff != nil {
		return utxoDiff, nil
	}

	utxoDiffCached, ok := uds.utxoDiffCache.Get(blockHash)
	if ok && utxoDiffCached != nil {
		return utxoDiffCached, nil
	}

	utxoDiffBytes, err := dbContext.Get(uds.utxoDiffHashAsKey(blockHash))
	if err != nil {
		return nil, err
	}

	utxoDiffDeserialized, err := uds.deserializeUTXODiff(utxoDiffBytes)
	if err != nil {
		return nil, err
	}

	uds.utxoDiffCache.Add(blockHash, utxoDiffDeserialized)
	return utxoDiffDeserialized, nil
}

// UTXODiffChild gets the utxoDiff child associated with the given blockHash
func (uds *utxoDiffStore) UTXODiffChild(dbContext model.DBReader, stagingArea *model.StagingArea, blockHash *externalapi.DomainHash) (*externalapi.DomainHash, error) {
	stagingShard := uds.stagingShard(stagingArea)

	utxoDiffChild, ok := stagingShard.utxoDiffChildToAdd[*blockHash]
	if ok && utxoDiffChild != nil {
		return utxoDiffChild, nil
	}
	utxoDiffChildCached, ok := uds.utxoDiffChildCache.Get(blockHash)
	if ok && utxoDiffChildCached != nil {
		return utxoDiffChildCached, nil
	}

	utxoDiffChildBytes, err := dbContext.Get(uds.utxoDiffChildHashAsKey(blockHash))
	if err != nil {
		return nil, err
	}

	utxoDiffChildDeserialized, err := uds.deserializeUTXODiffChild(utxoDiffChildBytes)
	if err != nil {
		return nil, err
	}
	uds.utxoDiffChildCache.Add(blockHash, utxoDiffChildDeserialized)
	return utxoDiffChild, nil
}

// HasUTXODiffChild returns true if the given blockHash has a UTXODiffChild
func (uds *utxoDiffStore) HasUTXODiffChild(dbContext model.DBReader, stagingArea *model.StagingArea, blockHash *externalapi.DomainHash) (bool, error) {
	stagingShard := uds.stagingShard(stagingArea)

	utxoDiff, ok := stagingShard.utxoDiffChildToAdd[*blockHash]
	if ok && utxoDiff != nil {
		return true, nil
	}

	if uds.utxoDiffChildCache.Has(blockHash) {
		return true, nil
	}

	return dbContext.Has(uds.utxoDiffChildHashAsKey(blockHash))
}

// Delete deletes the utxoDiff associated with the given blockHash
func (uds *utxoDiffStore) Delete(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash) {
	stagingShard := uds.stagingShard(stagingArea)
	uds.utxoDiffCache.Remove(blockHash)
	uds.utxoDiffChildCache.Remove(blockHash)

	if uds.isBlockHashStaged(stagingShard, blockHash) {
		delete(stagingShard.utxoDiffToAdd, *blockHash)
		delete(stagingShard.utxoDiffChildToAdd, *blockHash)
		return
	}
	stagingShard.toDelete[*blockHash] = struct{}{}
}

func (uds *utxoDiffStore) utxoDiffHashAsKey(hash *externalapi.DomainHash) model.DBKey {
	return uds.utxoDiffBucket.Key(hash.ByteSlice())
}

func (uds *utxoDiffStore) utxoDiffChildHashAsKey(hash *externalapi.DomainHash) model.DBKey {
	return uds.utxoDiffChildBucket.Key(hash.ByteSlice())
}

func (uds *utxoDiffStore) serializeUTXODiff(utxoDiff externalapi.UTXODiff) ([]byte, error) {
	dbUtxoDiff, err := serialization.UTXODiffToDBUTXODiff(utxoDiff)
	if err != nil {
		return nil, err
	}

	bytes, err := proto.Marshal(dbUtxoDiff)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	return bytes, nil
}

func (uds *utxoDiffStore) deserializeUTXODiff(utxoDiffBytes []byte) (externalapi.UTXODiff, error) {
	dbUTXODiff := &serialization.DbUtxoDiff{}
	err := proto.Unmarshal(utxoDiffBytes, dbUTXODiff)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	return serialization.DBUTXODiffToUTXODiff(dbUTXODiff)
}

func (uds *utxoDiffStore) serializeUTXODiffChild(utxoDiffChild *externalapi.DomainHash) ([]byte, error) {
	bytes, err := proto.Marshal(serialization.DomainHashToDbHash(utxoDiffChild))
	if err != nil {
		return nil, errors.WithStack(err)
	}
	return bytes, nil
}

func (uds *utxoDiffStore) deserializeUTXODiffChild(utxoDiffChildBytes []byte) (*externalapi.DomainHash, error) {
	dbHash := &serialization.DbHash{}
	err := proto.Unmarshal(utxoDiffChildBytes, dbHash)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	return serialization.DbHashToDomainHash(dbHash)
}
