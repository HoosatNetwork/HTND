package lrucachehashandwindowsizetoblockghostdagdatahashpairs

import (
	// "sync"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
)

type lruKey struct {
	blockHash  externalapi.DomainHash
	windowSize int
	// includeTrustedWindow separates the two windows a block can have. The difficulty path walks
	// into a pruned block's trusted DAA window; every other caller stops at the pruning boundary.
	// They are different answers for the same (hash, windowSize), so they must not share an entry -
	// see dagtraversalmanager.calculateBlockWindowHeap (HTN-204).
	includeTrustedWindow bool
}

func newKey(blockHash *externalapi.DomainHash, windowSize int, includeTrustedWindow bool) lruKey {
	return lruKey{
		blockHash:            *blockHash,
		windowSize:           windowSize,
		includeTrustedWindow: includeTrustedWindow,
	}
}

// LRUCache is a least-recently-used cache from
// lruKey to *externalapi.BlockGHOSTDAGDataHashPair
type LRUCache struct {
	// lock     *sync.RWMutex
	cache    map[lruKey][]*externalapi.BlockGHOSTDAGDataHashPair
	capacity int
}

// New creates a new LRUCache
func New(capacity int, preallocate bool) *LRUCache {
	var cache map[lruKey][]*externalapi.BlockGHOSTDAGDataHashPair
	if preallocate {
		cache = make(map[lruKey][]*externalapi.BlockGHOSTDAGDataHashPair, capacity+1)
	} else {
		cache = make(map[lruKey][]*externalapi.BlockGHOSTDAGDataHashPair)
	}
	return &LRUCache{
		// lock:     &sync.RWMutex{},
		cache:    cache,
		capacity: capacity,
	}
}

// Add adds an entry to the LRUCache
func (c *LRUCache) Add(blockHash *externalapi.DomainHash, windowSize int, includeTrustedWindow bool, value []*externalapi.BlockGHOSTDAGDataHashPair) {
	// c.lock.Lock()
	// defer c.lock.Unlock()
	key := newKey(blockHash, windowSize, includeTrustedWindow)
	c.cache[key] = value

	if len(c.cache) > c.capacity {
		c.evictRandom()
	}
}

// Get returns the entry for the given key, or (nil, false) otherwise
func (c *LRUCache) Get(blockHash *externalapi.DomainHash, windowSize int, includeTrustedWindow bool) ([]*externalapi.BlockGHOSTDAGDataHashPair, bool) {
	// c.lock.RLock()
	// defer c.lock.RUnlock()
	key := newKey(blockHash, windowSize, includeTrustedWindow)
	value, ok := c.cache[key]
	if !ok {
		return nil, false
	}
	return value, true
}

// Has returns whether the LRUCache contains the given key
func (c *LRUCache) Has(blockHash *externalapi.DomainHash, windowSize int, includeTrustedWindow bool) bool {
	// c.lock.RLock()
	// defer c.lock.RUnlock()
	key := newKey(blockHash, windowSize, includeTrustedWindow)
	dagdata, ok := c.cache[key]
	return ok && dagdata != nil
}

// Remove removes the entry for the the given key. Does nothing if
// the entry does not exist
func (c *LRUCache) Remove(blockHash *externalapi.DomainHash, windowSize int, includeTrustedWindow bool) {
	// c.lock.Lock()
	// defer c.lock.Unlock()
	key := newKey(blockHash, windowSize, includeTrustedWindow)
	delete(c.cache, key)
}

func (c *LRUCache) evictRandom() {
	var keyToEvict lruKey
	for key := range c.cache {
		keyToEvict = key
		break
	}
	// Delete the key itself rather than rebuilding one from its fields. Rebuilding was merely
	// pointless while the key was (hash, windowSize); once includeTrustedWindow joined it, a rebuild
	// that forgot the new field would evict a different entry than the one chosen - or none at all,
	// letting the cache grow past its capacity.
	delete(c.cache, keyToEvict)
}

func (c *LRUCache) Len() int {
	return len(c.cache)
}
