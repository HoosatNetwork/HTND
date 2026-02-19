package lrucachehashandwindowsizetoblockghostdagdatahashpairs

import (
	// "sync"

	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
)

type lruKey struct {
	blockHash  externalapi.DomainHash
	windowSize int
}

func newKey(blockHash *externalapi.DomainHash, windowSize int) lruKey {
	return lruKey{
		blockHash:  *blockHash,
		windowSize: windowSize,
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
func (c *LRUCache) Add(blockHash *externalapi.DomainHash, windowSize int, value []*externalapi.BlockGHOSTDAGDataHashPair) {
	// c.lock.Lock()
	// defer c.lock.Unlock()
	key := newKey(blockHash, windowSize)
	c.cache[key] = value

	if len(c.cache) > c.capacity {
		c.evictRandom()
	}
}

// Get returns the entry for the given key, or (nil, false) otherwise
func (c *LRUCache) Get(blockHash *externalapi.DomainHash, windowSize int) ([]*externalapi.BlockGHOSTDAGDataHashPair, bool) {
	// c.lock.RLock()
	// defer c.lock.RUnlock()
	key := newKey(blockHash, windowSize)
	value, ok := c.cache[key]
	if !ok {
		return nil, false
	}
	return value, true
}

// Has returns whether the LRUCache contains the given key
func (c *LRUCache) Has(blockHash *externalapi.DomainHash, windowSize int) bool {
	// c.lock.RLock()
	// defer c.lock.RUnlock()
	key := newKey(blockHash, windowSize)
	dagdata, ok := c.cache[key]
	return ok && dagdata != nil
}

// Remove removes the entry for the the given key. Does nothing if
// the entry does not exist
func (c *LRUCache) Remove(blockHash *externalapi.DomainHash, windowSize int) {
	// c.lock.Lock()
	// defer c.lock.Unlock()
	key := newKey(blockHash, windowSize)
	delete(c.cache, key)
}

func (c *LRUCache) evictRandom() {
	var keyToEvict lruKey
	for key := range c.cache {
		keyToEvict = key
		break
	}
	key := newKey(&keyToEvict.blockHash, keyToEvict.windowSize)
	delete(c.cache, key)
}
