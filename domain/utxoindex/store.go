package utxoindex

import (
	"encoding/binary"
	"slices"
	"sync"

	"github.com/Hoosat-Oy/HTND/domain/consensus/database/binaryserialization"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/infrastructure/db/database"
	"github.com/Hoosat-Oy/HTND/infrastructure/logger"
	"github.com/Hoosat-Oy/HTND/util/memory"
	"github.com/pkg/errors"
)

var (
	utxoIndexBucket          = database.MakeBucket([]byte("utxo-index"))
	utxoIndexCountsBucket    = database.MakeBucket([]byte("utxo-index-counts"))
	virtualParentsKey        = database.MakeBucket([]byte("")).Key([]byte("utxo-index-virtual-parents"))
	circulatingSupplyKey     = database.MakeBucket([]byte("")).Key([]byte("utxo-index-circulating-supply"))
	utxoCountsInitializedKey = database.MakeBucket([]byte("")).Key([]byte("utxo-index-counts-initialized"))
)

var utxoPairPool = sync.Pool{
	New: func() interface{} {
		return make([]UTXOPair, 0, 16)
	},
}

type utxoIndexStore struct {
	database database.Database
	toAdd    map[ScriptPublicKeyString]UTXOOutpointEntryPairs
	toRemove map[ScriptPublicKeyString]UTXOOutpointEntryPairs

	virtualParents []*externalapi.DomainHash
	// scriptCache    *scriptLRUCache
	// maxCacheSize   int
}

func newUTXOIndexStore(database database.Database) *utxoIndexStore {
	// Default cache size, can be made configurable
	const defaultCacheSize = 1000
	return &utxoIndexStore{
		database: database,
		toAdd:    make(map[ScriptPublicKeyString]UTXOOutpointEntryPairs),
		toRemove: make(map[ScriptPublicKeyString]UTXOOutpointEntryPairs),
		// scriptCache:  newScriptLRUCache(defaultCacheSize),
		// maxCacheSize: defaultCacheSize,
	}
}

// // scriptLRUCache is a thread-safe LRU cache for ScriptPublicKeyString -> []UTXOPair
// type scriptLRUCache struct {
// 	mu         sync.Mutex
// 	maxSize    int
// 	items      map[string]*scriptLRUNode
// 	head, tail *scriptLRUNode
// }

// type scriptLRUNode struct {
// 	key        string
// 	value      []UTXOPair
// 	prev, next *scriptLRUNode
// }

// func newScriptLRUCache(maxSize int) *scriptLRUCache {
// 	return &scriptLRUCache{
// 		maxSize: maxSize,
// 		items:   make(map[string]*scriptLRUNode),
// 	}
// }

// func (c *scriptLRUCache) Get(key string) ([]UTXOPair, bool) {
// 	c.mu.Lock()
// 	defer c.mu.Unlock()
// 	node, ok := c.items[key]
// 	if !ok {
// 		return nil, false
// 	}
// 	c.moveToFront(node)
// 	return node.value, true
// }

// func (c *scriptLRUCache) Put(key string, value []UTXOPair) {
// 	c.mu.Lock()
// 	defer c.mu.Unlock()
// 	// Don't cache large slices to avoid high allocations
// 	if len(value) > 100 {
// 		return
// 	}
// 	copiedValue := copyUTXOPairs(value)
// 	if node, ok := c.items[key]; ok {
// 		node.value = copiedValue
// 		c.moveToFront(node)
// 		return
// 	}
// 	node := &scriptLRUNode{key: key, value: copiedValue}
// 	c.items[key] = node
// 	c.addToFront(node)
// 	if len(c.items) > c.maxSize {
// 		c.evict()
// 	}
// }

// func (c *scriptLRUCache) Delete(key string) {
// 	c.mu.Lock()
// 	defer c.mu.Unlock()
// 	node, ok := c.items[key]
// 	if !ok {
// 		return
// 	}
// 	c.remove(node)
// 	delete(c.items, key)
// }

// func (c *scriptLRUCache) Clear() {
// 	c.mu.Lock()
// 	defer c.mu.Unlock()
// 	c.items = make(map[string]*scriptLRUNode)
// 	c.head = nil
// 	c.tail = nil
// }

// func (c *scriptLRUCache) moveToFront(node *scriptLRUNode) {
// 	if c.head == node {
// 		return
// 	}
// 	c.remove(node)
// 	c.addToFront(node)
// }

// func (c *scriptLRUCache) addToFront(node *scriptLRUNode) {
// 	node.prev = nil
// 	node.next = c.head
// 	if c.head != nil {
// 		c.head.prev = node
// 	}
// 	c.head = node
// 	if c.tail == nil {
// 		c.tail = node
// 	}
// }

// func (c *scriptLRUCache) remove(node *scriptLRUNode) {
// 	if node.prev != nil {
// 		node.prev.next = node.next
// 	} else {
// 		c.head = node.next
// 	}
// 	if node.next != nil {
// 		node.next.prev = node.prev
// 	} else {
// 		c.tail = node.prev
// 	}
// }

// func (c *scriptLRUCache) evict() {
// 	if c.tail == nil {
// 		return
// 	}
// 	key := c.tail.key
// 	c.remove(c.tail)
// 	delete(c.items, key)
// }

// copyUTXOPairs creates a copy of UTXOPair slice to prevent slice mutation.
// Note: This performs a shallow copy of the slice structure. The individual
// UTXOPair elements are copied by value, but nested pointers (e.g., UTXOEntry
// interface implementations) are shared. This is safe because UTXOEntry
// implementations are treated as immutable in the codebase.
func copyUTXOPairs(pairs []UTXOPair) []UTXOPair {
	if pairs == nil {
		return nil
	}
	return slices.Clone(pairs)
}

func (uis *utxoIndexStore) add(scriptPublicKey *externalapi.ScriptPublicKey, outpoint *externalapi.DomainOutpoint, utxoEntry externalapi.UTXOEntry) error {
	key := ScriptPublicKeyString(scriptPublicKey.String())
	log.Tracef("Adding outpoint %s:%d to scriptPublicKey %s",
		outpoint.TransactionID, outpoint.Index, key)

	// Invalidate the per-script cache for this key
	// uis.scriptCache.Delete(string(key))

	// If the outpoint exists in `toRemove` simply remove it from there and return
	if toRemoveOutpointsOfKey, ok := uis.toRemove[key]; ok {
		if _, ok := toRemoveOutpointsOfKey[*outpoint]; ok {
			log.Tracef("Outpoint %s:%d exists in `toRemove`. Deleting it from there",
				outpoint.TransactionID, outpoint.Index)
			delete(toRemoveOutpointsOfKey, *outpoint)
			return nil
		}
	}

	// Create a UTXOOutpointEntryPairs entry in `toAdd` if it doesn't exist
	if _, ok := uis.toAdd[key]; !ok {
		log.Tracef("Creating key %s in `toAdd`", key)
		uis.toAdd[key] = make(UTXOOutpointEntryPairs)
	}

	// Return an error if the outpoint already exists in `toAdd`
	toAddPairsOfKey := uis.toAdd[key]
	if _, ok := toAddPairsOfKey[*outpoint]; ok {
		return errors.Errorf("cannot add outpoint %s because it's being added already", outpoint)
	}
	toAddPairsOfKey[*outpoint] = utxoEntry
	log.Tracef("Added outpoint %s:%d to scriptPublicKey %s",
		outpoint.TransactionID, outpoint.Index, key)
	return nil
}

func (uis *utxoIndexStore) remove(scriptPublicKey *externalapi.ScriptPublicKey, outpoint *externalapi.DomainOutpoint, utxoEntry externalapi.UTXOEntry) error {
	key := ScriptPublicKeyString(scriptPublicKey.String())
	log.Tracef("Removing outpoint %s:%d from scriptPublicKey %s",
		outpoint.TransactionID, outpoint.Index, key)

	// Invalidate the per-script cache for this key
	// uis.scriptCache.Delete(string(key))

	// If the outpoint exists in `toAdd` simply remove it from there and return
	if toAddPairsOfKey, ok := uis.toAdd[key]; ok {
		if _, ok := toAddPairsOfKey[*outpoint]; ok {
			log.Tracef("Outpoint %s:%d exists in `toAdd`. Deleting it from there",
				outpoint.TransactionID, outpoint.Index)
			delete(toAddPairsOfKey, *outpoint)
			return nil
		}
	}

	// Create a UTXOOutpointEntryPair in `toRemove` if it doesn't exist
	if _, ok := uis.toRemove[key]; !ok {
		log.Tracef("Creating key %s in `toRemove`", key)
		uis.toRemove[key] = make(UTXOOutpointEntryPairs)
	}

	// Return an error if the outpoint already exists in `toRemove`
	toRemovePairsOfKey := uis.toRemove[key]
	if _, ok := toRemovePairsOfKey[*outpoint]; ok {
		return errors.Errorf("cannot remove outpoint %s because it's being removed already", outpoint)
	}

	toRemovePairsOfKey[*outpoint] = utxoEntry

	log.Tracef("Removed outpoint %s:%d from scriptPublicKey %s",
		outpoint.TransactionID, outpoint.Index, key)
	return nil
}

func (uis *utxoIndexStore) updateVirtualParents(virtualParents []*externalapi.DomainHash) {
	uis.virtualParents = virtualParents
}

func (uis *utxoIndexStore) discard() {
	for k := range uis.toAdd {
		delete(uis.toAdd, k)
	}
	for k := range uis.toRemove {
		delete(uis.toRemove, k)
	}
	uis.virtualParents = nil
	// uis.scriptCache.Clear()
}

func (uis *utxoIndexStore) commit() error {
	onEnd := logger.LogAndMeasureExecutionTime(log, "utxoIndexStore.commit")
	defer onEnd()

	dbTransaction, err := uis.database.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = dbTransaction.RollbackUnlessClosed() }()

	countDeltas := make(map[ScriptPublicKeyString]int64, len(uis.toAdd)+len(uis.toRemove))

	toRemoveSompiSupply := uint64(0)

	for scriptPublicKeyString, toRemoveUTXOOutpointEntryPairs := range uis.toRemove {
		countDeltas[scriptPublicKeyString] -= int64(len(toRemoveUTXOOutpointEntryPairs))
		scriptPublicKey := externalapi.NewScriptPublicKeyFromString(string(scriptPublicKeyString))
		bucket := uis.bucketForScriptPublicKey(scriptPublicKey)
		// Invalidate per-script cache for this key
		// uis.scriptCache.Delete(string(scriptPublicKeyString))
		for outpointToRemove, utxoEntryToRemove := range toRemoveUTXOOutpointEntryPairs {
			key, err := uis.convertOutpointToKey(bucket, &outpointToRemove)
			if err != nil {
				return err
			}
			err = dbTransaction.Delete(key)
			if err != nil {
				return err
			}
			toRemoveSompiSupply = toRemoveSompiSupply + utxoEntryToRemove.Amount()
		}
	}

	toAddSompiSupply := uint64(0)

	for scriptPublicKeyString, toAddUTXOOutpointEntryPairs := range uis.toAdd {
		countDeltas[scriptPublicKeyString] += int64(len(toAddUTXOOutpointEntryPairs))
		scriptPublicKey := externalapi.NewScriptPublicKeyFromString(string(scriptPublicKeyString))
		bucket := uis.bucketForScriptPublicKey(scriptPublicKey)
		// Invalidate per-script cache for this key
		// uis.scriptCache.Delete(string(scriptPublicKeyString))
		for outpointToAdd, utxoEntryToAdd := range toAddUTXOOutpointEntryPairs {
			key, err := uis.convertOutpointToKey(bucket, &outpointToAdd)
			if err != nil {
				return err
			}
			serializedUTXOEntry, err := serializeUTXOEntry(utxoEntryToAdd)
			if err != nil {
				return err
			}
			err = dbTransaction.Put(key, serializedUTXOEntry)
			if err != nil {
				return err
			}
			toAddSompiSupply = toAddSompiSupply + utxoEntryToAdd.Amount()
		}
	}

	serializeParentHashes := serializeHashes(uis.virtualParents)
	err = dbTransaction.Put(virtualParentsKey, serializeParentHashes)
	if err != nil {
		return err
	}

	err = uis.updateCirculatingSompiSupply(dbTransaction, toAddSompiSupply, toRemoveSompiSupply)
	if err != nil {
		return err
	}

	err = uis.applyUTXOCountDeltas(dbTransaction, countDeltas)
	if err != nil {
		return err
	}

	err = dbTransaction.Put(utxoCountsInitializedKey, []byte{1})
	if err != nil {
		return err
	}

	err = dbTransaction.Commit()
	if err != nil {
		return err
	}

	uis.discard()
	return nil
}

func (uis *utxoIndexStore) addAndCommitOutpointsWithoutTransaction(utxoPairs []*externalapi.OutpointAndUTXOEntryPair) error {
	var (
		wg         sync.WaitGroup
		errChan    = make(chan error, len(utxoPairs))
		amountChan = make(chan uint64, len(utxoPairs))
	)
	countDeltas := make(map[ScriptPublicKeyString]int64)
	for _, pair := range utxoPairs {
		countDeltas[ScriptPublicKeyString(pair.UTXOEntry.ScriptPublicKey().String())]++
	}

	for _, pair := range utxoPairs {
		wg.Add(1)
		go func(pair *externalapi.OutpointAndUTXOEntryPair) {
			defer wg.Done()

			bucket := uis.bucketForScriptPublicKey(pair.UTXOEntry.ScriptPublicKey())
			key, err := uis.convertOutpointToKey(bucket, pair.Outpoint)
			if err != nil {
				errChan <- err
				return
			}

			serializedUTXOEntry, err := serializeUTXOEntry(pair.UTXOEntry)
			if err != nil {
				errChan <- err
				return
			}

			err = uis.database.Put(key, serializedUTXOEntry)
			if err != nil {
				errChan <- err
				return
			}

			amountChan <- pair.UTXOEntry.Amount()
		}(pair)
	}

	// Wait for all goroutines to finish
	wg.Wait()
	close(errChan)
	close(amountChan)

	// Check for any errors
	for err := range errChan {
		if err != nil {
			return err
		}
	}

	// Sum all amounts
	toAddSompiSupply := uint64(0)
	for amount := range amountChan {
		toAddSompiSupply += amount
	}

	err := uis.applyUTXOCountDeltasToAccessor(uis.database, countDeltas)
	if err != nil {
		return err
	}

	err = uis.database.Put(utxoCountsInitializedKey, []byte{1})
	if err != nil {
		return err
	}

	// Final update
	return uis.updateCirculatingSompiSupplyWithoutTransaction(toAddSompiSupply, 0)
}

func (uis *utxoIndexStore) updateAndCommitVirtualParentsWithoutTransaction(virtualParents []*externalapi.DomainHash) error {
	serializeParentHashes := serializeHashes(virtualParents)
	err := uis.database.Put(virtualParentsKey, serializeParentHashes)
	if err != nil {
		return err
	}
	return uis.database.Put(utxoCountsInitializedKey, []byte{1})
}

func (uis *utxoIndexStore) bucketForScriptPublicKey(scriptPublicKey *externalapi.ScriptPublicKey) *database.Bucket {
	return utxoIndexBucket.Bucket(uis.scriptPublicKeyBytes(scriptPublicKey))
}

func (uis *utxoIndexStore) scriptPublicKeyBytes(scriptPublicKey *externalapi.ScriptPublicKey) []byte {
	scriptLen := len(scriptPublicKey.Script)
	if scriptLen <= 64 {
		var arr [66]byte
		binary.LittleEndian.PutUint16(arr[:2], scriptPublicKey.Version)
		copy(arr[2:2+scriptLen], scriptPublicKey.Script)
		return arr[:2+scriptLen]
	}
	scriptPublicKeyBytes := make([]byte, 2+scriptLen)
	binary.LittleEndian.PutUint16(scriptPublicKeyBytes[:2], scriptPublicKey.Version)
	copy(scriptPublicKeyBytes[2:], scriptPublicKey.Script)
	return scriptPublicKeyBytes
}

func (uis *utxoIndexStore) utxoCountKeyForScriptPublicKey(scriptPublicKey *externalapi.ScriptPublicKey) *database.Key {
	return utxoIndexCountsBucket.Key(uis.scriptPublicKeyBytes(scriptPublicKey))
}

func (uis *utxoIndexStore) convertOutpointToKey(bucket *database.Bucket, outpoint *externalapi.DomainOutpoint) (*database.Key, error) {
	serializedOutpoint, err := serializeOutpoint(outpoint)
	if err != nil {
		return nil, err
	}
	return bucket.Key(serializedOutpoint), nil
}

func (uis *utxoIndexStore) convertKeyToOutpoint(key *database.Key) (*externalapi.DomainOutpoint, error) {
	serializedOutpoint := key.Suffix()
	return deserializeOutpoint(serializedOutpoint)
}

func (uis *utxoIndexStore) stagedData() (
	toAdd []UTXOPair,
	toRemove []UTXOPair,
	virtualParents []*externalapi.DomainHash,
) {
	// Flatten uis.toAdd map to []UTXOPair
	for _, utxoPairs := range uis.toAdd {
		for outpoint, entry := range utxoPairs {
			toAdd = append(toAdd, UTXOPair{Outpoint: outpoint, Entry: entry})
		}
	}
	// Flatten uis.toRemove map to []UTXOPair
	for _, utxoPairs := range uis.toRemove {
		for outpoint, entry := range utxoPairs {
			toRemove = append(toRemove, UTXOPair{Outpoint: outpoint, Entry: entry})
		}
	}
	return toAdd, toRemove, uis.virtualParents
}

func (uis *utxoIndexStore) isAnythingStaged() bool {
	return len(uis.toAdd) > 0 || len(uis.toRemove) > 0
}

// UTXOPair is a struct for streaming UTXO results efficiently
type UTXOPair struct {
	Outpoint externalapi.DomainOutpoint
	Entry    externalapi.UTXOEntry
}

func (uis *utxoIndexStore) PaginatedUTXOs(scriptPublicKey *externalapi.ScriptPublicKey, offset uint32, limit uint32, buffer *memory.Block[UTXOPair]) ([]UTXOPair, *memory.Block[UTXOPair], error) {
	if uis.isAnythingStaged() {
		return nil, buffer, errors.Errorf("cannot get UTXOs while staging isn't empty")
	}

	bucket := uis.bucketForScriptPublicKey(scriptPublicKey)
	cursor, err := uis.database.Cursor(bucket)
	if err != nil {
		return nil, buffer, err
	}
	defer cursor.Close()

	iterator := uint32(0)
	count := 0
	if limit == 0 {
		for ok := cursor.First(); ok; ok = cursor.Next() {
			if iterator > offset {
				count++
			}
			iterator++
		}
	} else {
		count = int(limit)
	}

	slice := buffer.Slice()[:0]
	if cap(slice) < count {
		buffer = memory.Realloc(buffer, count)
	}
	slice = buffer.Slice()[:0]

	iterator = uint32(0) // reset iterator to reuse for filling buffer
	for ok := cursor.First(); ok; ok = cursor.Next() {
		if iterator > offset {
			key, err := cursor.Key()
			if err != nil {
				return nil, buffer, err
			}
			outpoint, err := uis.convertKeyToOutpoint(key)
			if err != nil {
				return nil, buffer, err
			}
			serializedUTXOEntry, err := cursor.Value()
			if err != nil {
				return nil, buffer, err
			}
			utxoEntry, err := deserializeUTXOEntry(serializedUTXOEntry)
			if err != nil {
				return nil, buffer, err
			}
			slice = append(slice, UTXOPair{Outpoint: *outpoint, Entry: utxoEntry})
			if limit > 0 && uint32(len(slice)) >= limit {
				break
			}
		}
		iterator++
	}
	return slice, buffer, nil
}

// UTXOs streams UTXOs for a ScriptPublicKey directly into a slice (allocation-efficient)
func (uis *utxoIndexStore) UTXOs(scriptPublicKey *externalapi.ScriptPublicKey, limit uint32, buffer *memory.Block[UTXOPair]) ([]UTXOPair, *memory.Block[UTXOPair], error) {
	if uis.isAnythingStaged() {
		return nil, buffer, errors.Errorf("cannot get UTXOs while staging isn't empty")
	}

	// scriptKeyString := scriptPublicKey.String()
	// if cachedPairs, ok := uis.scriptCache.Get(scriptKeyString); ok {
	// 	return cachedPairs, nil
	// }

	bucket := uis.bucketForScriptPublicKey(scriptPublicKey)
	cursor, err := uis.database.Cursor(bucket)
	if err != nil {
		return nil, buffer, err
	}
	defer cursor.Close()

	// First pass: count entries
	count := 0
	if limit == 0 {
		for ok := cursor.First(); ok; ok = cursor.Next() {
			count++
		}
	} else {
		count = int(limit)
	}

	slice := buffer.Slice()[:0]
	// Preallocate exactly to avoid reallocations during append
	if cap(slice) < count {
		buffer = memory.Realloc(buffer, count) // reallocate if existing capacity is insufficient
	}
	slice = buffer.Slice()[:0]

	// Second pass: fill
	for ok := cursor.First(); ok; ok = cursor.Next() {
		key, err := cursor.Key()
		if err != nil {
			return nil, buffer, err
		}
		outpoint, err := uis.convertKeyToOutpoint(key)
		if err != nil {
			return nil, buffer, err
		}
		serializedUTXOEntry, err := cursor.Value()
		if err != nil {
			return nil, buffer, err
		}
		utxoEntry, err := deserializeUTXOEntry(serializedUTXOEntry)
		if err != nil {
			return nil, buffer, err
		}
		slice = append(slice, UTXOPair{Outpoint: *outpoint, Entry: utxoEntry})
		if limit > 0 && uint32(len(slice)) >= limit {
			break
		}
	}

	// uis.scriptCache.Put(scriptKeyString, buffer)
	return slice, buffer, nil
}

// UTXOs streams UTXOs for a ScriptPublicKey directly into a slice (allocation-efficient)
func (uis *utxoIndexStore) HasUTXOs(scriptPublicKey *externalapi.ScriptPublicKey) (bool, error) {
	if uis.isAnythingStaged() {
		return false, errors.Errorf("cannot get UTXOs while staging isn't empty")
	}

	return uis.database.Has(uis.utxoCountKeyForScriptPublicKey(scriptPublicKey))
}

// GetBalance returns the total balance for a ScriptPublicKey without allocating full UTXO entries
func (uis *utxoIndexStore) GetBalance(scriptPublicKey *externalapi.ScriptPublicKey) (uint64, error) {
	if uis.isAnythingStaged() {
		return 0, errors.Errorf("cannot get balance while staging isn't empty")
	}

	bucket := uis.bucketForScriptPublicKey(scriptPublicKey)
	cursor, err := uis.database.Cursor(bucket)
	if err != nil {
		return 0, err
	}
	defer cursor.Close()

	var balance uint64
	for ok := cursor.First(); ok; ok = cursor.Next() {
		serializedUTXOEntry, err := cursor.Value()
		if err != nil {
			return 0, err
		}
		amount, err := deserializeUTXOAmount(serializedUTXOEntry)
		if err != nil {
			return 0, err
		}
		balance += amount
	}
	return balance, nil
}

func (uis *utxoIndexStore) getVirtualParents() ([]*externalapi.DomainHash, error) {
	if uis.isAnythingStaged() {
		return nil, errors.Errorf("cannot get the virtual parents while staging isn't empty")
	}

	serializedHashes, err := uis.database.Get(virtualParentsKey)
	// if database.IsNotFoundError(err) {
	// 	log.Infof("getVirtualParents failed to retrieve with %s\n", virtualParentsKey)
	// 	return nil, err
	// }
	if err != nil {
		return nil, err
	}

	return deserializeHashes(serializedHashes)
}

func (uis *utxoIndexStore) deleteAll() error {
	// First we delete the virtual parents, so if anything goes wrong, the UTXO index will be marked as "not synced"
	// and will be reset.
	err := uis.database.Delete(virtualParentsKey)
	if err != nil {
		return err
	}

	err = uis.database.Delete(circulatingSupplyKey)
	if err != nil {
		return err
	}

	err = uis.database.Delete(utxoCountsInitializedKey)
	if err != nil {
		return err
	}

	countsCursor, err := uis.database.Cursor(utxoIndexCountsBucket)
	if err != nil {
		return err
	}
	defer countsCursor.Close()
	for countsCursor.Next() {
		key, err := countsCursor.Key()
		if err != nil {
			return err
		}

		err = uis.database.Delete(key)
		if err != nil {
			return err
		}
	}

	cursor, err := uis.database.Cursor(utxoIndexBucket)
	if err != nil {
		return err
	}
	defer cursor.Close()
	for cursor.Next() {
		key, err := cursor.Key()
		if err != nil {
			return err
		}

		err = uis.database.Delete(key)
		if err != nil {
			return err
		}
	}

	// Clear both caches after deleting all data
	// uis.scriptCache.Clear()

	return nil
}

func (uis *utxoIndexStore) initializeCirculatingSompiSupply() error {
	cursor, err := uis.database.Cursor(utxoIndexBucket)
	if err != nil {
		return err
	}
	defer cursor.Close()

	circulatingSompiSupplyInDatabase := uint64(0)
	for cursor.Next() {
		serializedUTXOEntry, err := cursor.Value()
		if err != nil {
			return err
		}
		utxoAmount, err := deserializeUTXOAmount(serializedUTXOEntry)
		if err != nil {
			return err
		}

		circulatingSompiSupplyInDatabase = circulatingSompiSupplyInDatabase + utxoAmount
	}

	err = uis.database.Put(
		circulatingSupplyKey,
		binaryserialization.SerializeUint64(circulatingSompiSupplyInDatabase),
	)
	if err != nil {
		return err
	}

	return nil
}

func (uis *utxoIndexStore) updateCirculatingSompiSupply(dbTransaction database.Transaction, toAddSompiSupply uint64, toRemoveSompiSupply uint64) error {
	if toAddSompiSupply != toRemoveSompiSupply {
		circulatingSupplyBytes, err := dbTransaction.Get(circulatingSupplyKey)
		if database.IsNotFoundError(err) {
			log.Infof("updateCirculatingSompiSupply failed to retrieve with %s\n", circulatingSupplyKey)
			return err
		}
		if err != nil {
			return err
		}

		circulatingSupply, err := binaryserialization.DeserializeUint64(circulatingSupplyBytes)
		if err != nil {
			return err
		}
		err = dbTransaction.Put(
			circulatingSupplyKey,
			binaryserialization.SerializeUint64(circulatingSupply+toAddSompiSupply-toRemoveSompiSupply),
		)
		if err != nil {
			return err
		}
	}
	return nil
}

func (uis *utxoIndexStore) updateCirculatingSompiSupplyWithoutTransaction(toAddSompiSupply uint64, toRemoveSompiSupply uint64) error {
	if toAddSompiSupply != toRemoveSompiSupply {
		circulatingSupplyBytes, err := uis.database.Get(circulatingSupplyKey)
		if database.IsNotFoundError(err) {
			log.Infof("updateCirculatingSompiSupplyWithoutTransaction failed to retrieve with %s\n", circulatingSupplyKey)
			return err
		}
		if err != nil {
			return err
		}

		circulatingSupply, err := binaryserialization.DeserializeUint64(circulatingSupplyBytes)
		if err != nil {
			return err
		}
		err = uis.database.Put(
			circulatingSupplyKey,
			binaryserialization.SerializeUint64(circulatingSupply+toAddSompiSupply-toRemoveSompiSupply),
		)
		if err != nil {
			return err
		}
	}
	return nil
}

func (uis *utxoIndexStore) getCirculatingSompiSupply() (uint64, error) {
	if uis.isAnythingStaged() {
		return 0, errors.Errorf("cannot get circulatingSupply while staging isn't empty")
	}
	circulatingSupply, err := uis.database.Get(circulatingSupplyKey)
	if database.IsNotFoundError(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return binaryserialization.DeserializeUint64(circulatingSupply)
}

func (uis *utxoIndexStore) applyUTXOCountDeltas(tx database.Transaction, countDeltas map[ScriptPublicKeyString]int64) error {
	return uis.applyUTXOCountDeltasToAccessor(tx, countDeltas)
}

func (uis *utxoIndexStore) applyUTXOCountDeltasToAccessor(accessor database.DataAccessor, countDeltas map[ScriptPublicKeyString]int64) error {
	for scriptPublicKeyString, delta := range countDeltas {
		if delta == 0 {
			continue
		}

		scriptPublicKey := externalapi.NewScriptPublicKeyFromString(string(scriptPublicKeyString))
		countKey := uis.utxoCountKeyForScriptPublicKey(scriptPublicKey)

		currentCount := uint64(0)
		countBytes, err := accessor.Get(countKey)
		if err != nil {
			if !database.IsNotFoundError(err) {
				return err
			}
		} else {
			currentCount, err = binaryserialization.DeserializeUint64(countBytes)
			if err != nil {
				return err
			}
		}

		newCount := int64(currentCount) + delta
		if newCount < 0 {
			return errors.Errorf("utxo count for scriptPublicKey %s became negative", scriptPublicKeyString)
		}

		if newCount == 0 {
			err = accessor.Delete(countKey)
			if err != nil {
				return err
			}
			continue
		}

		err = accessor.Put(countKey, binaryserialization.SerializeUint64(uint64(newCount)))
		if err != nil {
			return err
		}
	}

	return nil
}
