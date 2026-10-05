package consensusstatestore

import (
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/utxolrucache"
	"github.com/HoosatNetwork/HTND/v2/util/staging"
)

var importingPruningPointUTXOSetKeyName = []byte("importing-pruning-point-utxo-set")

// consensusStateStore represents a store for the current consensus state
type consensusStateStore struct {
	shardID                         model.StagingShardID
	virtualUTXOSetCache             *utxolrucache.LRUCache
	tipsCache                       []*externalapi.DomainHash
	tipsKey                         model.DBKey
	utxoSetBucket                   model.DBBucket
	utxoSetKeyPrefix                []byte
	utxoSetKeyLimit                 []byte
	importingPruningPointUTXOSetKey model.DBKey
}

// New instantiates a new ConsensusStateStore
func New(prefixBucket model.DBBucket, utxoSetCacheSize int, preallocate bool) model.ConsensusStateStore {
	utxoSetBucket := prefixBucket.Bucket(utxoSetBucketName)
	// Copy the prefix. Key encoding reads it outside the consensus lock, and the bucket's
	// path slice must not be aliased by that.
	utxoSetKeyPrefix := append([]byte(nil), utxoSetBucket.Path()...)
	return &consensusStateStore{
		shardID:                         staging.GenerateShardingID(),
		virtualUTXOSetCache:             utxolrucache.New(utxoSetCacheSize, preallocate),
		tipsKey:                         prefixBucket.Key(tipsKeyName),
		importingPruningPointUTXOSetKey: prefixBucket.Key(importingPruningPointUTXOSetKeyName),
		utxoSetBucket:                   utxoSetBucket,
		utxoSetKeyPrefix:                utxoSetKeyPrefix,
		utxoSetKeyLimit:                 prefixLimit(utxoSetKeyPrefix),
	}
}

func (css *consensusStateStore) IsStaged(stagingArea *model.StagingArea) bool {
	return css.stagingShard(stagingArea).isStaged()
}

func (css *consensusStateStore) UnstageAll(stagingArea *model.StagingArea) {
	stagingShard := css.stagingShard(stagingArea)
	stagingShard.tipsStaging = nil
	stagingShard.virtualUTXODiffStaging = nil
}

func (css *consensusStateStore) CacheLen() int {
	return css.virtualUTXOSetCache.Len()
}
