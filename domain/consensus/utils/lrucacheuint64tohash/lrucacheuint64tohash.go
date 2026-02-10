package lrucacheuint64tohash

import (
	"container/list"

	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
)

type entry struct {
	key   uint64
	value *externalapi.DomainHash
	elem  *list.Element
}

type LRUCache struct {
	cache    map[uint64]*entry
	lru      *list.List
	capacity int
}

func New(capacity int, preallocate bool) *LRUCache {
	cache := make(map[uint64]*entry)
	if preallocate {
		cache = make(map[uint64]*entry, capacity+capacity/4)
	}
	return &LRUCache{
		cache:    cache,
		lru:      list.New(),
		capacity: capacity,
	}
}

func (c *LRUCache) Add(key uint64, value *externalapi.DomainHash) {
	if e, ok := c.cache[key]; ok {
		e.value = value
		c.lru.MoveToFront(e.elem)
		return
	}
	e := &entry{
		key:   key,
		value: value,
	}
	e.elem = c.lru.PushFront(e)
	c.cache[key] = e
	if c.lru.Len() > c.capacity {
		c.evict()
	}
}

func (c *LRUCache) Get(key uint64) (*externalapi.DomainHash, bool) {
	e, ok := c.cache[key]
	if !ok {
		return nil, false
	}
	c.lru.MoveToFront(e.elem)
	return e.value, true
}

func (c *LRUCache) Has(key uint64) bool {
	e, ok := c.cache[key]
	return ok && e.value != nil
}

func (c *LRUCache) Remove(key uint64) {
	e, ok := c.cache[key]
	if !ok {
		return
	}
	c.lru.Remove(e.elem)
	delete(c.cache, key)
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
	c.cache = make(map[uint64]*entry, len(c.cache)/2+1)
	c.lru.Init()
}
