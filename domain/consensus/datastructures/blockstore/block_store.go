package blockstore

import (
	"sync"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/database"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/database/serialization"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/lrucache"
	"github.com/HoosatNetwork/HTND/v2/util/memory"
	"github.com/HoosatNetwork/HTND/v2/util/staging"
	"github.com/pkg/errors"
)

var bucketName = []byte("blocks")

// blockStore represents a store of blocks
type blockStore struct {
	shardID model.StagingShardID
	lock    sync.Mutex
	// cache holds blocks serialized, outside the Go heap, with each block's PoW hash as the entry's
	// meta. It used to hold decoded blocks, bounded only by entry count; once ML-DSA-44 transactions
	// arrived - ~3.7 KB of signature and public key per input - 10,000 cached testnet blocks were over
	// 3 GB of decoded objects, the live heap sat at GOMEMLIMIT, and the garbage collector took ~60% of
	// the CPU during virtual resolution. See lrucache.OffHeap.
	//
	// The PoW hash is not part of DbBlock, so it does not survive serialization, and the cache is the
	// only place it outlives the block's arrival. It must come back with every cache hit: relay will
	// not announce a block without one (relayBlock), and a peer that receives a relayed block without
	// one bans the connection (handleRelayInvsFlow). A block read from the database has none, as
	// before.
	cache *lrucache.OffHeap[string]
	// existsCache remembers blocks HasBlock found in the database, without their bodies. HasBlock
	// asks about the key only, so answering it needs neither the block nor a slot in cache.
	existsCache *lrucache.LRUCache[struct{}]
	countCached uint64
	bucket      model.DBBucket
	countKey    model.DBKey
}

// New instantiates a new BlockStore. Its block cache holds at most cacheSize blocks whose serialized
// sizes add up to at most cacheByteBudget bytes.
func New(dbContext model.DBReader, prefixBucket model.DBBucket, cacheSize int, cacheByteBudget int,
	preallocate bool,
) (model.BlockStore, error) {
	blockStore := &blockStore{
		shardID:     staging.GenerateShardingID(),
		cache:       lrucache.NewOffHeap[string](cacheSize, cacheByteBudget, preallocate),
		existsCache: lrucache.New[struct{}](cacheSize, preallocate),
		bucket:      prefixBucket.Bucket(bucketName),
		countKey:    prefixBucket.Key([]byte("blocks-count")),
	}

	err := blockStore.initializeCount(dbContext)
	if err != nil {
		return nil, err
	}

	return blockStore, nil
}

func (bs *blockStore) initializeCount(dbContext model.DBReader) error {
	count := uint64(0)
	hasCountBytes, err := dbContext.Has(bs.countKey)
	if err != nil {
		return err
	}
	if hasCountBytes {
		countBytes, err := dbContext.Get(bs.countKey)
		if database.IsNotFoundError(err) {
			log.Infof("initializeCount failed to retrieve with %s\n", bs.countKey)
			return err
		}
		if err != nil {
			return err
		}
		count, err = bs.deserializeBlockCount(countBytes)
		if err != nil {
			return err
		}
	}
	bs.lock.Lock()
	bs.countCached = count
	bs.lock.Unlock()
	return nil
}

// Stage stages the given block for the given blockHash
func (bs *blockStore) Stage(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash, block *externalapi.DomainBlock) {
	stagingShard := bs.stagingShard(stagingArea)
	stagingShard.toAdd[*blockHash] = block.Clone()
}

func (bs *blockStore) IsStaged(stagingArea *model.StagingArea) bool {
	return bs.stagingShard(stagingArea).isStaged()
}

func (bs *blockStore) UnstageAll(stagingArea *model.StagingArea) {
	stagingShard := bs.stagingShard(stagingArea)
	stagingShard.toAdd = make(map[externalapi.DomainHash]*externalapi.DomainBlock)
	stagingShard.toDelete = make(map[externalapi.DomainHash]struct{})
}

// Block gets the block associated with the given blockHash
func (bs *blockStore) Block(dbContext model.DBReader, stagingArea *model.StagingArea, blockHash *externalapi.DomainHash) (*externalapi.DomainBlock, error) {
	stagingShard := bs.stagingShard(stagingArea)

	return bs.block(dbContext, stagingShard, blockHash)
}

func (bs *blockStore) block(dbContext model.DBReader, stagingShard *blockStagingShard, blockHash *externalapi.DomainHash) (*externalapi.DomainBlock, error) {
	block, ok := stagingShard.toAdd[*blockHash]
	if ok && block != nil {
		// log.Infof("Block found %s from staging", blockHash)
		return block.Clone(), nil
	}

	return bs.committedBlock(dbContext, blockHash, true)
}

// BlockWithoutCaching gets a committed block without adding it to the block cache. It is the read
// for callers that do not hold the consensus lock.
//
// Block puts a block it read from the database into the cache. Without the lock, a commit that
// deletes the block (pruning) can land between that read and the Add: the commit removes the cache
// entry, and the Add then puts the deleted block back. HasBlock answers from the cache first, so
// consensus would see a pruned body as present until it was evicted. Not caching closes that
// window, and keeps a scan over every stored block from evicting the blocks consensus is using.
func (bs *blockStore) BlockWithoutCaching(dbContext model.DBReader, blockHash *externalapi.DomainHash) (
	*externalapi.DomainBlock, error,
) {
	return bs.committedBlock(dbContext, blockHash, false)
}

// committedBlock reads a block from the cache, or from the database on a miss, adding it to the cache
// when addToCache is set.
func (bs *blockStore) committedBlock(dbContext model.DBReader, blockHash *externalapi.DomainHash, addToCache bool) (
	*externalapi.DomainBlock, error,
) {
	var cachedBlock *externalapi.DomainBlock
	hit, err := bs.cache.Decode(blockHash, func(blockBytes []byte, powHash string) error {
		var err error
		cachedBlock, err = bs.deserializeBlock(blockBytes)
		if err != nil {
			return err
		}
		cachedBlock.PoWHash = powHash
		return nil
	})
	if hit {
		return cachedBlock, err
	}

	blockBytes, err := dbContext.Get(bs.hashAsKey(blockHash))
	if err != nil {
		return nil, err
	}

	// The decoded block is not cached, so the caller can own it without a clone.
	blockDeserialized, err := bs.deserializeBlock(blockBytes)
	if err != nil {
		return nil, err
	}
	if addToCache {
		bs.cache.Add(blockHash, blockBytes, blockDeserialized.PoWHash)
	}
	return blockDeserialized, nil
}

// HasBlock returns whether a block with a given hash exists in the store.
func (bs *blockStore) HasBlock(dbContext model.DBReader, stagingArea *model.StagingArea, blockHash *externalapi.DomainHash) (bool, error) {
	stagingShard := bs.stagingShard(stagingArea)
	block, ok := stagingShard.toAdd[*blockHash]
	if ok && block != nil {
		return true, nil
	}

	return bs.committedHasBlock(dbContext, blockHash, true)
}

// HasBlockWithoutCaching reports whether a committed block exists without remembering the answer in
// existsCache, for callers that do not hold the consensus lock.
//
// A commit removes a deleted block from the caches before its database transaction is applied
// (staging.CommitAllChanges commits every shard, then the transaction). A lock-free reader checking
// the database in that gap still finds the key, and remembering it would leave the deleted block in
// existsCache, reported present to consensus until evicted. Reading both caches is safe: the block
// cache has its own lock, and existsCache is used only under the store's lock.
func (bs *blockStore) HasBlockWithoutCaching(dbContext model.DBReader, blockHash *externalapi.DomainHash) (bool, error) {
	return bs.committedHasBlock(dbContext, blockHash, false)
}

// committedHasBlock answers HasBlock from the caches, or from the database on a miss, remembering a
// found block in existsCache when addToCache is set.
func (bs *blockStore) committedHasBlock(dbContext model.DBReader, blockHash *externalapi.DomainHash, addToCache bool) (
	bool, error,
) {
	cachedHas := bs.cache.Has(blockHash)
	if !cachedHas {
		bs.lock.Lock()
		_, cachedHas = bs.existsCache.Get(blockHash)
		bs.lock.Unlock()
	}
	if cachedHas {
		return true, nil
	}

	// A found block is remembered in existsCache, by callers holding the consensus lock, so consensus
	// asking about the same block again is a hit. Only the key is remembered: no caller of HasBlock
	// uses the block, so there is nothing to copy or deserialize, and no block cache slot to take from
	// a block that is in use.
	has, err := dbContext.Has(bs.hashAsKey(blockHash))
	// A database fault is an error, not a missing block.
	if err != nil {
		return false, err
	}
	if !has {
		return false, nil
	}

	if addToCache {
		bs.lock.Lock()
		bs.existsCache.Add(blockHash, struct{}{})
		bs.lock.Unlock()
	}
	return true, nil
}

// Blocks gets the blocks associated with the given blockHashes
func (bs *blockStore) Blocks(dbContext model.DBReader, stagingArea *model.StagingArea, blockHashes []*externalapi.DomainHash) ([]*externalapi.DomainBlock, error) {
	stagingShard := bs.stagingShard(stagingArea)

	blocks := make([]*externalapi.DomainBlock, len(blockHashes))
	for i, hash := range blockHashes {
		var err error
		blocks[i], err = bs.block(dbContext, stagingShard, hash)
		if err != nil {
			return nil, err
		}
	}
	return blocks, nil
}

// Delete deletes the block associated with the given blockHash
func (bs *blockStore) Delete(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash) {
	stagingShard := bs.stagingShard(stagingArea)
	bs.cache.Remove(blockHash)
	bs.lock.Lock()
	bs.existsCache.Remove(blockHash)
	bs.lock.Unlock()

	if _, ok := stagingShard.toAdd[*blockHash]; ok {
		delete(stagingShard.toAdd, *blockHash)
		return
	}
	stagingShard.toDelete[*blockHash] = struct{}{}
}

// serializeBlockOffHeap serializes block into a buffer outside the Go heap, for the commit to write
// to the database and then hand to the cache. With ML-DSA-44 inputs a testnet block is ~250 KB that
// would otherwise be serialized onto the heap. See lrucache.MarshalOffHeap.
func (bs *blockStore) serializeBlockOffHeap(block *externalapi.DomainBlock) (*memory.Block[byte], error) {
	return lrucache.MarshalOffHeap(serialization.DomainBlockToDbBlock(block))
}

func (bs *blockStore) deserializeBlock(blockBytes []byte) (*externalapi.DomainBlock, error) {
	dbBlock := &serialization.DbBlock{}
	err := dbBlock.UnmarshalVT(blockBytes)
	if err != nil {
		return nil, err
	}
	return serialization.DbBlockToDomainBlock(dbBlock)
}

func (bs *blockStore) hashAsKey(hash *externalapi.DomainHash) model.DBKey {
	return bs.bucket.Key(hash.ByteSlice())
}

func (bs *blockStore) Count(stagingArea *model.StagingArea) uint64 {
	stagingShard := bs.stagingShard(stagingArea)
	return bs.count(stagingShard)
}

func (bs *blockStore) count(stagingShard *blockStagingShard) uint64 {
	bs.lock.Lock()
	countCached := bs.countCached
	bs.lock.Unlock()
	return countCached + uint64(len(stagingShard.toAdd)) - uint64(len(stagingShard.toDelete))
}

func (bs *blockStore) deserializeBlockCount(countBytes []byte) (uint64, error) {
	dbBlockCount := &serialization.DbBlockCount{}
	err := dbBlockCount.UnmarshalVT(countBytes)
	if err != nil {
		return 0, err
	}
	return dbBlockCount.Count, nil
}

func (bs *blockStore) serializeBlockCount(count uint64) ([]byte, error) {
	dbBlockCount := &serialization.DbBlockCount{Count: count}
	return dbBlockCount.MarshalVT()
}

type allBlockHashesIterator struct {
	cursor   model.DBCursor
	isClosed bool
}

func (a *allBlockHashesIterator) First() bool {
	if a.isClosed {
		panic("Tried using a closed AllBlockHashesIterator")
	}
	return a.cursor.First()
}

func (a *allBlockHashesIterator) Next() bool {
	if a.isClosed {
		panic("Tried using a closed AllBlockHashesIterator")
	}
	return a.cursor.Next()
}

func (a *allBlockHashesIterator) Get() (*externalapi.DomainHash, error) {
	if a.isClosed {
		return nil, errors.New("Tried using a closed AllBlockHashesIterator")
	}
	key, err := a.cursor.Key()
	if err != nil {
		return nil, err
	}

	blockHashBytes := key.Suffix()
	return externalapi.NewDomainHashFromByteSlice(blockHashBytes)
}

func (a *allBlockHashesIterator) Close() error {
	if a.isClosed {
		return errors.New("Tried using a closed AllBlockHashesIterator")
	}
	a.isClosed = true
	err := a.cursor.Close()
	if err != nil {
		return err
	}
	a.cursor = nil
	return nil
}

func (bs *blockStore) AllBlockHashesIterator(dbContext model.DBReader) (model.BlockIterator, error) {
	cursor, err := dbContext.Cursor(bs.bucket)
	if err != nil {
		return nil, err
	}

	return &allBlockHashesIterator{cursor: cursor}, nil
}

func (bs *blockStore) CacheLen() int {
	return bs.cache.Len()
}
