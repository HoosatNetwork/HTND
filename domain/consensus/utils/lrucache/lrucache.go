package lrucache

import (
	"container/list"

	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/cespare/xxhash/v2"
)

// entry stores the value + LRU metadata
type entry[V any] struct {
	key   externalapi.DomainHash // full key for collision safety
	value V
	elem  *list.Element // LRU list pointer
}

// LRUCache is a fast, generic LRU cache using hashed uint64 keys internally
type LRUCache[V any] struct {
	// mu       sync.RWMutex       // uncomment for concurrent usage
	cache    map[uint64]*entry[V]
	lru      *list.List
	capacity int
}

// New creates a new LRUCache
func New[V any](capacity int, preallocate bool) *LRUCache[V] {
	cache := make(map[uint64]*entry[V])
	if preallocate {
		cache = make(map[uint64]*entry[V], capacity+capacity/4) // slight over-allocation helps avoid early resizes
	}
	return &LRUCache[V]{
		cache:    cache,
		lru:      list.New(),
		capacity: capacity,
	}
}

// hash computes fast non-crypto hash (xxHash is excellent quality + very fast)
func hash(key externalapi.DomainHash) uint64 {
	return xxhash.Sum64(key.ByteSlice())
}

// Add adds an entry (updates LRU position if already exists)
func (c *LRUCache[V]) Add(key *externalapi.DomainHash, value V) {
	// c.mu.Lock()
	// defer c.mu.Unlock()

	k := *key
	h := hash(k)

	if e, ok := c.cache[h]; ok {
		// update in-place if collision check passes
		if e.key == k {
			e.value = value
			c.lru.MoveToFront(e.elem)
			return
		}
		// rare collision → treat as new (old one stays until evicted)
	}

	e := &entry[V]{
		key:   k,
		value: value,
	}
	e.elem = c.lru.PushFront(e)
	c.cache[h] = e

	if c.lru.Len() > c.capacity {
		c.evict()
	}
}

// Get returns the entry or (zero-value, false)
func (c *LRUCache[V]) Get(key *externalapi.DomainHash) (V, bool) {
	// c.mu.RLock()
	// defer c.mu.RUnlock()

	h := hash(*key)

	e, ok := c.cache[h]
	if !ok {
		var zero V
		return zero, false
	}

	// collision check
	if e.key != *key {
		var zero V
		return zero, false
	}

	// promote to MRU
	// c.mu.Lock()   // if using lock, need write lock here in real concurrent version
	c.lru.MoveToFront(e.elem)
	// c.mu.Unlock()

	return e.value, true
}

// Has checks existence (no LRU promotion)
func (c *LRUCache[V]) Has(key *externalapi.DomainHash) bool {
	// c.mu.RLock()
	// defer c.mu.RUnlock()

	h := hash(*key)
	e, ok := c.cache[h]
	return ok && e.key == *key
}

// Remove removes entry if exists
func (c *LRUCache[V]) Remove(key *externalapi.DomainHash) {
	// c.mu.Lock()
	// defer c.mu.Unlock()

	h := hash(*key)
	e, ok := c.cache[h]
	if !ok || e.key != *key {
		return
	}

	c.lru.Remove(e.elem)
	delete(c.cache, h)
}

// evict removes LRU item
func (c *LRUCache[V]) evict() {
	if c.lru.Len() == 0 {
		return
	}
	back := c.lru.Back()
	if back == nil {
		return
	}
	e := back.Value.(*entry[V])
	c.lru.Remove(back)
	delete(c.cache, hash(e.key))
}

// Clear empties the cache
func (c *LRUCache[V]) Clear() {
	// c.mu.Lock()
	// defer c.mu.Unlock()
	c.cache = make(map[uint64]*entry[V], len(c.cache)/2+1) // shrink a bit
	c.lru.Init()
}
