package acceptancedatastore

import (
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/database"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/database/serialization"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/lrucache"
	"github.com/HoosatNetwork/HTND/v2/util/memory"
	"github.com/HoosatNetwork/HTND/v2/util/staging"
	"github.com/pkg/errors"
)

var bucketName = []byte("acceptance-data")

// acceptanceDataStore represents a store of AcceptanceData
type acceptanceDataStore struct {
	shardID model.StagingShardID
	// cache holds acceptance data serialized, outside the Go heap (see lrucache.OffHeap). Acceptance
	// data carries every merged transaction whole, ML-DSA-44 signatures included, and the decoded
	// cache this replaced held ~300 MB of it on the heap and deep-cloned it on every hit.
	//
	// What serialization drops comes back empty from a hit, as it always did from a miss: the
	// transactions' cached Fee, Mass and ID. Fee is kept per transaction in TransactionAcceptanceData,
	// and no reader takes Fee or Mass from these transactions (RPC recomputes a zero mass).
	cache  *lrucache.OffHeap[struct{}]
	bucket model.DBBucket
}

// New instantiates a new AcceptanceDataStore. Its cache holds at most cacheSize entries whose
// serialized sizes add up to at most cacheByteBudget bytes.
func New(prefixBucket model.DBBucket, cacheSize int, cacheByteBudget int, preallocate bool) model.AcceptanceDataStore {
	return &acceptanceDataStore{
		shardID: staging.GenerateShardingID(),
		cache:   lrucache.NewOffHeap[struct{}](cacheSize, cacheByteBudget, preallocate),
		bucket:  prefixBucket.Bucket(bucketName),
	}
}

// Stage stages the given acceptanceData for the given blockHash
func (ads *acceptanceDataStore) Stage(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash, acceptanceData externalapi.AcceptanceData) {
	stagingShard := ads.stagingShard(stagingArea)
	stagingShard.toAdd[*blockHash] = acceptanceData.Clone()
}

func (ads *acceptanceDataStore) IsStaged(stagingArea *model.StagingArea) bool {
	return ads.stagingShard(stagingArea).isStaged()
}

func (ads *acceptanceDataStore) UnstageAll(stagingArea *model.StagingArea) {
	stagingShard := ads.stagingShard(stagingArea)
	stagingShard.toAdd = make(map[externalapi.DomainHash]externalapi.AcceptanceData)
	stagingShard.toDelete = make(map[externalapi.DomainHash]struct{})
}

// Get gets the acceptanceData associated with the given blockHash
func (ads *acceptanceDataStore) Get(dbContext model.DBReader, stagingArea *model.StagingArea, blockHash *externalapi.DomainHash) (externalapi.AcceptanceData, error) {
	stagingShard := ads.stagingShard(stagingArea)
	acceptanceData, ok := stagingShard.toAdd[*blockHash]
	if ok && acceptanceData != nil {
		return acceptanceData.Clone(), nil
	}
	var cached externalapi.AcceptanceData
	hit, err := ads.cache.Decode(blockHash, func(acceptanceDataBytes []byte, _ struct{}) error {
		var err error
		cached, err = ads.deserializeAcceptanceData(acceptanceDataBytes)
		return err
	})
	if hit {
		return cached, err
	}

	acceptanceDataBytes, err := dbContext.Get(ads.hashAsKey(blockHash))
	if database.IsNotFoundError(err) {
		return nil, errors.Wrapf(err, "initializeCount failed to retrieve with %s", blockHash)
	}
	if err != nil {
		return nil, err
	}

	// The decoded value is not cached, so the caller can own it without a clone.
	acceptanceDataDeserialized, err := ads.deserializeAcceptanceData(acceptanceDataBytes)
	if err != nil {
		return nil, err
	}
	ads.cache.Add(blockHash, acceptanceDataBytes, struct{}{})
	return acceptanceDataDeserialized, nil
}

// Delete deletes the acceptanceData associated with the given blockHash
func (ads *acceptanceDataStore) Delete(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash) {
	stagingShard := ads.stagingShard(stagingArea)
	ads.cache.Remove(blockHash)
	if _, ok := stagingShard.toAdd[*blockHash]; ok {
		delete(stagingShard.toAdd, *blockHash)
		return
	}
	stagingShard.toDelete[*blockHash] = struct{}{}
}

// serializeAcceptanceDataOffHeap serializes acceptanceData into a buffer outside the Go heap, for the
// commit to write to the database and then hand to the cache. See lrucache.MarshalOffHeap.
func (ads *acceptanceDataStore) serializeAcceptanceDataOffHeap(acceptanceData externalapi.AcceptanceData) (
	*memory.Block[byte], error,
) {
	return lrucache.MarshalOffHeap(serialization.DomainAcceptanceDataToDbAcceptanceData(acceptanceData))
}

func (ads *acceptanceDataStore) deserializeAcceptanceData(acceptanceDataBytes []byte) (externalapi.AcceptanceData, error) {
	dbAcceptanceData := &serialization.DbAcceptanceData{}
	err := dbAcceptanceData.UnmarshalVT(acceptanceDataBytes)
	if err != nil {
		return nil, err
	}
	return serialization.DbAcceptanceDataToDomainAcceptanceData(dbAcceptanceData)
}

func (ads *acceptanceDataStore) hashAsKey(hash *externalapi.DomainHash) model.DBKey {
	return ads.bucket.Key(hash.ByteSlice())
}

func (ads *acceptanceDataStore) CacheLen() int {
	return ads.cache.Len()
}
