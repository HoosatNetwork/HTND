package lrucachehashpairtoblockghostdagdatahashpair

import (
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
)

// lruKey identifies a cache entry
type lruKey struct {
	blockHash externalapi.DomainHash
	index     uint64
}

// entry holds the value + intrusive doubly-linked list pointers
type entry struct {
	key   lruKey
	value *externalapi.BlockGHOSTDAGDataHashPair

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
	length   int // explicit count → Len() is O(1)
}

// New creates a new LRU cache
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

// moveToFront promotes the entry to MRU (head) position
func (c *LRUCache) moveToFront(e *entry) {
	if e == c.head {
		return
	}

	// unlink
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

	// link to front
	e.prev = nil
	e.next = c.head
	if c.head != nil {
		c.head.prev = e
	}
	c.head = e

	// first item case
	if c.tail == nil {
		c.tail = e
	}
}

// Add inserts or updates the value and promotes to front
func (c *LRUCache) Add(blockHash *externalapi.DomainHash, index uint64, value *externalapi.BlockGHOSTDAGDataHashPair) {
	k := lruKey{
		blockHash: *blockHash,
		index:     index,
	}

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

// Get returns the value if present and promotes to MRU
func (c *LRUCache) Get(blockHash *externalapi.DomainHash, index uint64) (*externalapi.BlockGHOSTDAGDataHashPair, bool) {
	k := lruKey{
		blockHash: *blockHash,
		index:     index,
	}

	e, ok := c.cache[k]
	if !ok {
		return nil, false
	}

	// Note: original had e.value == nil check — kept for safety, but usually not needed
	if e.value == nil {
		return nil, false
	}

	c.moveToFront(e)
	return e.value, true
}

// Has checks existence without promotion
func (c *LRUCache) Has(blockHash *externalapi.DomainHash, index uint64) bool {
	k := lruKey{
		blockHash: *blockHash,
		index:     index,
	}
	e, ok := c.cache[k]
	return ok && e.value != nil
}

// Remove deletes the entry if it exists
func (c *LRUCache) Remove(blockHash *externalapi.DomainHash, index uint64) {
	k := lruKey{
		blockHash: *blockHash,
		index:     index,
	}

	e, ok := c.cache[k]
	if !ok {
		return
	}

	c.unlink(e)
	delete(c.cache, k)
	c.length--
}

// evict removes the least recently used entry (tail)
func (c *LRUCache) evict() {
	if c.tail == nil {
		return
	}

	e := c.tail
	c.unlink(e)
	delete(c.cache, e.key)
	c.length--
}

// unlink removes node from the list (does not delete from map)
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

	// help GC slightly
	e.prev = nil
	e.next = nil
}

// Clear empties the cache
func (c *LRUCache) Clear() {
	c.cache = make(map[lruKey]*entry, c.capacity>>1)
	c.head = nil
	c.tail = nil
	c.length = 0
}

// Len returns current number of items
func (c *LRUCache) Len() int {
	return c.length
}
