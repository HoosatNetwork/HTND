package dagtraversalmanager

import (
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
)

// entry is both the value holder and the list node (intrusive style)
type blockWindowCacheEntry struct {
	key   string
	value []*externalapi.DomainHash

	prev *blockWindowCacheEntry
	next *blockWindowCacheEntry
}

type blockWindowLRUCache struct {
	cache    map[string]*blockWindowCacheEntry
	head     *blockWindowCacheEntry
	tail     *blockWindowCacheEntry
	capacity int
	length   int
}

func newBlockWindowLRUCache(capacity int) *blockWindowLRUCache {
	return &blockWindowLRUCache{
		cache:    make(map[string]*blockWindowCacheEntry),
		capacity: capacity,
	}
}

// moveToFront promotes entry to most-recently-used position
func (c *blockWindowLRUCache) moveToFront(e *blockWindowCacheEntry) {
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

func (c *blockWindowLRUCache) get(key string) ([]*externalapi.DomainHash, bool) {
	e, exists := c.cache[key]
	if !exists {
		return nil, false
	}

	c.moveToFront(e)
	return e.value, true
}

func (c *blockWindowLRUCache) put(key string, value []*externalapi.DomainHash) {
	if e, ok := c.cache[key]; ok {
		// hit → update value + promote
		e.value = value
		c.moveToFront(e)
		return
	}

	// new entry
	e := &blockWindowCacheEntry{
		key:   key,
		value: value,
	}

	c.moveToFront(e) // also sets head/tail correctly for first item
	c.cache[key] = e
	c.length++

	if c.length > c.capacity {
		c.evict()
	}
}

// evict removes the least recently used item
func (c *blockWindowLRUCache) evict() {
	if c.tail == nil {
		return
	}

	e := c.tail
	c.unlink(e)
	delete(c.cache, e.key)
	c.length--
}

// unlink removes node from the linked list (does not free or delete from map)
func (c *blockWindowLRUCache) unlink(e *blockWindowCacheEntry) {
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
