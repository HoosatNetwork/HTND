package lrucachehashandwindowsizetoblockghostdagdatahashpairs

import (
	"container/list"

	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
)

type lruKey struct {
	blockHash  externalapi.DomainHash
	windowSize int
}

type entry struct {
	key   lruKey
	value []*externalapi.BlockGHOSTDAGDataHashPair
	elem  *list.Element
}

type LRUCache struct {
	cache    map[lruKey]*entry
	lru      *list.List
	capacity int
}

func New(capacity int, preallocate bool) *LRUCache {
	cache := make(map[lruKey]*entry)
	if preallocate {
		cache = make(map[lruKey]*entry, capacity+capacity/4)
	}
	return &LRUCache{
		cache:    cache,
		lru:      list.New(),
		capacity: capacity,
	}
}

func newKey(blockHash *externalapi.DomainHash, windowSize int) lruKey {
	return lruKey{
		blockHash:  *blockHash,
		windowSize: windowSize,
	}
}

func (c *LRUCache) Add(blockHash *externalapi.DomainHash, windowSize int, value []*externalapi.BlockGHOSTDAGDataHashPair) {
	k := newKey(blockHash, windowSize)
	if e, ok := c.cache[k]; ok {
		e.value = value
		c.lru.MoveToFront(e.elem)
		return
	}
	e := &entry{
		key:   k,
		value: value,
	}
	e.elem = c.lru.PushFront(e)
	c.cache[k] = e
	if c.lru.Len() > c.capacity {
		c.evict()
	}
}

func (c *LRUCache) Get(blockHash *externalapi.DomainHash, windowSize int) ([]*externalapi.BlockGHOSTDAGDataHashPair, bool) {
	k := newKey(blockHash, windowSize)
	e, ok := c.cache[k]
	if !ok {
		return nil, false
	}
	c.lru.MoveToFront(e.elem)
	return e.value, true
}

func (c *LRUCache) Has(blockHash *externalapi.DomainHash, windowSize int) bool {
	k := newKey(blockHash, windowSize)
	e, ok := c.cache[k]
	return ok && e.value != nil
}

func (c *LRUCache) Remove(blockHash *externalapi.DomainHash, windowSize int) {
	k := newKey(blockHash, windowSize)
	e, ok := c.cache[k]
	if !ok {
		return
	}
	c.lru.Remove(e.elem)
	delete(c.cache, k)
}

func (c *LRUCache) evict() {
	if c.lru.Len() == 0 {
		return
	}
	back := c.lru.Back()
	if back == nil {
		return
	}
	e := back.Value.(*entry)
	c.lru.Remove(back)
	delete(c.cache, e.key)
}

func (c *LRUCache) Clear() {
	c.cache = make(map[lruKey]*entry, len(c.cache)/2+1)
	c.lru.Init()
}

func (c *LRUCache) Len() int {
	return c.lru.Len()
}
