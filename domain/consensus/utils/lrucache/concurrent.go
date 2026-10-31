package lrucache

import (
	"sync"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
)

// ConcurrentLRUCache is an LRUCache whose methods take a mutex, for the caches a goroutine
// other than the consensus lock holder mutates: the status, multiset, UTXO diff (+child) and
// DAA (score, added blocks) caches, from which history deletion evicts without the consensus
// lock. Every other LRUCache user keeps the lock-free LRUCache (0f6f764f3 removed its mutex
// for speed); this wrapper leaves that code path untouched. The mutex is a leaf: nothing is
// called under it but the LRUCache method.
type ConcurrentLRUCache[V any] struct {
	mu    sync.Mutex
	cache *LRUCache[V]
}

// NewConcurrent creates a ConcurrentLRUCache; the arguments are New's.
func NewConcurrent[V any](capacity int, preallocate bool) *ConcurrentLRUCache[V] {
	return &ConcurrentLRUCache[V]{cache: New[V](capacity, preallocate)}
}

// Add is LRUCache.Add under the mutex.
func (c *ConcurrentLRUCache[V]) Add(key *externalapi.DomainHash, value V) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache.Add(key, value)
}

// Get is LRUCache.Get under the mutex.
func (c *ConcurrentLRUCache[V]) Get(key *externalapi.DomainHash) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cache.Get(key)
}

// Has is LRUCache.Has under the mutex.
func (c *ConcurrentLRUCache[V]) Has(key *externalapi.DomainHash) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cache.Has(key)
}

// Remove is LRUCache.Remove under the mutex.
func (c *ConcurrentLRUCache[V]) Remove(key *externalapi.DomainHash) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache.Remove(key)
}

// Clear is LRUCache.Clear under the mutex.
func (c *ConcurrentLRUCache[V]) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache.Clear()
}

// Len is LRUCache.Len under the mutex.
func (c *ConcurrentLRUCache[V]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cache.Len()
}
