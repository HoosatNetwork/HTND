package lrucache

import (
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/cespare/xxhash/v2"
)

// entry is both the value holder and the list node (intrusive style)
type entry[V any] struct {
	key   externalapi.DomainHash
	value V

	prev *entry[V]
	next *entry[V]
}

// LRUCache is a thread-unsafe (for now) generic LRU cache using intrusive links
type LRUCache[V any] struct {
	cache    map[uint64]*entry[V]
	head     *entry[V]
	tail     *entry[V]
	capacity int
	length   int
}

// New creates a new LRU cache
func New[V any](capacity int, preallocate bool) *LRUCache[V] {
	m := make(map[uint64]*entry[V])
	if preallocate {
		m = make(map[uint64]*entry[V], capacity+(capacity>>2)) // ~25% headroom
	}

	return &LRUCache[V]{
		cache:    m,
		capacity: capacity,
	}
}

// hash computes a fast non-cryptographic hash of the key
func hash(key externalapi.DomainHash) uint64 {
	return xxhash.Sum64(key.ByteSlice())
}

// moveToFront promotes entry to most-recently-used position
func (c *LRUCache[V]) moveToFront(e *entry[V]) {
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

// Add inserts or updates a key-value pair (moves to front on update)
func (c *LRUCache[V]) Add(key *externalapi.DomainHash, value V) {
	h := hash(*key)

	if e, ok := c.cache[h]; ok {
		if e.key == *key {
			// hit → update value + promote
			e.value = value
			c.moveToFront(e)
			return
		}
		// hash collision but different key → fall through (treat as new)
	}

	// new entry
	e := &entry[V]{
		key:   *key,
		value: value,
	}

	c.moveToFront(e) // also sets head/tail correctly for first item
	c.cache[h] = e
	c.length++

	if c.length > c.capacity {
		c.evict()
	}
}

// Get returns the value if present and promotes it to MRU
func (c *LRUCache[V]) Get(key *externalapi.DomainHash) (value V, ok bool) {
	h := hash(*key)

	e, exists := c.cache[h]
	if !exists {
		return
	}

	if e.key != *key {
		return // hash collision → not found
	}

	c.moveToFront(e)
	return e.value, true
}

// Has checks existence without changing LRU order
func (c *LRUCache[V]) Has(key *externalapi.DomainHash) bool {
	h := hash(*key)
	e, ok := c.cache[h]
	return ok && e.key == *key
}

// Remove deletes an entry if it exists
func (c *LRUCache[V]) Remove(key *externalapi.DomainHash) {
	h := hash(*key)
	e, ok := c.cache[h]
	if !ok || e.key != *key {
		return
	}

	c.unlink(e)
	delete(c.cache, h)
	c.length--
}

// evict removes the least recently used item
func (c *LRUCache[V]) evict() {
	if c.tail == nil {
		return
	}

	e := c.tail
	c.unlink(e)
	delete(c.cache, hash(e.key))
	c.length--
}

// unlink removes node from the linked list (does not free or delete from map)
func (c *LRUCache[V]) unlink(e *entry[V]) {
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

	// help GC a tiny bit
	e.prev = nil
	e.next = nil
}

// Clear empties the cache
func (c *LRUCache[V]) Clear() {
	// We let old entries be GC'd naturally
	c.cache = make(map[uint64]*entry[V], c.capacity>>1)
	c.head = nil
	c.tail = nil
	c.length = 0
}

// Len returns current number of items
func (c *LRUCache[V]) Len() int {
	return c.length
}
