package lrucacheghostdagdata

import (
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
)

// lruKey identifies a cache entry (hash + trusted flag)
type lruKey struct {
	blockHash     externalapi.DomainHash
	isTrustedData bool
}

// entry holds the value + intrusive list pointers
type entry struct {
	key   lruKey
	value *externalapi.BlockGHOSTDAGData

	prev *entry
	next *entry
}

// LRUCache is an intrusive-list based LRU cache (thread-unsafe)
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

// moveToFront promotes an entry to MRU position
func (c *LRUCache) moveToFront(e *entry) {
	if e == c.head {
		return
	}

	// unlink
	if e.prev != nil {
		e.prev.next = e.next
	}
	if e.next != nil {
		e.next.prev = e.prev
	}
	if e == c.tail {
		c.tail = e.prev
	}

	// link to front
	e.next = c.head
	e.prev = nil
	if c.head != nil {
		c.head.prev = e
	}
	c.head = e

	if c.tail == nil {
		c.tail = e // first item
	}
}

// Add inserts or updates the value for the given key (promotes to front)
func (c *LRUCache) Add(blockHash *externalapi.DomainHash, isTrustedData bool, value *externalapi.BlockGHOSTDAGData) {
	k := lruKey{
		blockHash:     *blockHash,
		isTrustedData: isTrustedData,
	}

	if e, ok := c.cache[k]; ok {
		e.value = value
		c.moveToFront(e)
		return
	}

	// new entry
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

// Get returns the value if present and promotes it to MRU
func (c *LRUCache) Get(blockHash *externalapi.DomainHash, isTrustedData bool) (*externalapi.BlockGHOSTDAGData, bool) {
	k := lruKey{
		blockHash:     *blockHash,
		isTrustedData: isTrustedData,
	}

	e, ok := c.cache[k]
	if !ok {
		return nil, false
	}

	c.moveToFront(e)
	return e.value, true
}

// Has checks existence without promotion
func (c *LRUCache) Has(blockHash *externalapi.DomainHash, isTrustedData bool) bool {
	k := lruKey{
		blockHash:     *blockHash,
		isTrustedData: isTrustedData,
	}
	_, ok := c.cache[k]
	return ok
}

// Remove deletes the entry if it exists
func (c *LRUCache) Remove(blockHash *externalapi.DomainHash, isTrustedData bool) {
	k := lruKey{
		blockHash:     *blockHash,
		isTrustedData: isTrustedData,
	}

	e, ok := c.cache[k]
	if !ok {
		return
	}

	c.unlink(e)
	delete(c.cache, k)
	c.length--
}

// evict removes the LRU (tail) entry
func (c *LRUCache) evict() {
	if c.tail == nil {
		return
	}

	e := c.tail
	c.unlink(e)
	delete(c.cache, e.key)
	c.length--
}

// unlink removes the node from the list (does not delete from map)
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

	// clear links to help GC slightly
	e.prev = nil
	e.next = nil
}

// Clear empties the cache
func (c *LRUCache) Clear() {
	// new map lets old entries GC naturally
	c.cache = make(map[lruKey]*entry, c.capacity>>1)
	c.head = nil
	c.tail = nil
	c.length = 0
}

// Len returns the current number of items
func (c *LRUCache) Len() int {
	return c.length
}
