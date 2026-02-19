package lrucachehashandwindowsizetoblockghostdagdatahashpairs

import (
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
)

// lruKey identifies a cache entry
type lruKey struct {
	blockHash  externalapi.DomainHash
	windowSize int
}

// entry holds the value + intrusive doubly-linked list pointers
type entry struct {
	key   lruKey
	value []*externalapi.BlockGHOSTDAGDataHashPair

	prev *entry
	next *entry
}

// LRUCache is an intrusive doubly-linked list based LRU cache.
// Not safe for concurrent use without external synchronization.
type LRUCache struct {
	cache    map[lruKey]*entry
	head     *entry // most recently used
	tail     *entry // least recently used
	capacity int
	length   int // O(1) Len()
}

// New creates a new LRU cache.
func New(capacity int, preallocate bool) *LRUCache {
	m := make(map[lruKey]*entry)
	if preallocate {
		m = make(map[lruKey]*entry, capacity+(capacity>>2)) // ~25% headroom
	}
	return &LRUCache{
		cache:    m,
		capacity: capacity,
	}
}

// moveToFront promotes entry to MRU (head) position.
func (c *LRUCache) moveToFront(e *entry) {
	if e == c.head {
		return
	}

	// Unlink from current position
	if e.prev != nil {
		e.prev.next = e.next
	} else {
		c.head = e.next // was head
	}
	if e.next != nil {
		e.next.prev = e.prev
	} else {
		c.tail = e.prev // was tail
	}

	// Link to front
	e.prev = nil
	e.next = c.head
	if c.head != nil {
		c.head.prev = e
	}
	c.head = e

	// If this is the first item
	if c.tail == nil {
		c.tail = e
	}
}

// Add inserts or updates and promotes to front. Evicts if over capacity.
func (c *LRUCache) Add(blockHash *externalapi.DomainHash, windowSize int, value []*externalapi.BlockGHOSTDAGDataHashPair) {
	k := lruKey{blockHash: *blockHash, windowSize: windowSize}

	if e, ok := c.cache[k]; ok {
		e.value = value
		c.moveToFront(e)
		return
	}

	e := &entry{
		key:   k,
		value: value,
	}

	c.moveToFront(e)
	c.cache[k] = e
	c.length++

	if c.length > c.capacity {
		c.evict()
	}
}

// Get returns value if present and promotes to MRU.
func (c *LRUCache) Get(blockHash *externalapi.DomainHash, windowSize int) ([]*externalapi.BlockGHOSTDAGDataHashPair, bool) {
	k := lruKey{blockHash: *blockHash, windowSize: windowSize}
	e, ok := c.cache[k]
	if !ok {
		return nil, false
	}
	c.moveToFront(e)
	return e.value, true
}

// Has checks existence without promotion.
func (c *LRUCache) Has(blockHash *externalapi.DomainHash, windowSize int) bool {
	k := lruKey{blockHash: *blockHash, windowSize: windowSize}
	_, ok := c.cache[k]
	return ok
}

// Remove deletes entry if exists.
func (c *LRUCache) Remove(blockHash *externalapi.DomainHash, windowSize int) {
	k := lruKey{blockHash: *blockHash, windowSize: windowSize}
	e, ok := c.cache[k]
	if !ok {
		return
	}
	c.unlink(e)
	delete(c.cache, k)
	c.length--
}

// evict removes the LRU (tail) entry.
func (c *LRUCache) evict() {
	if c.tail == nil {
		return
	}
	e := c.tail
	c.unlink(e)
	delete(c.cache, e.key)
	c.length--
}

// unlink removes node from list (does not delete from map).
func (c *LRUCache) unlink(e *entry) {
	if e.prev != nil {
		e.prev.next = e.next
	} else {
		c.head = e.next
	}
	if e.next != nil {
		e.next.prev = e.prev
	} else {
		c.tail = e.prev
	}
	// Help GC a bit
	e.prev = nil
	e.next = nil
}

// Clear empties the cache (old entries become GC-eligible).
func (c *LRUCache) Clear() {
	c.cache = make(map[lruKey]*entry, c.capacity>>1)
	c.head = nil
	c.tail = nil
	c.length = 0
}

// Len returns current item count.
func (c *LRUCache) Len() int {
	return c.length
}
