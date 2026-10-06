package lrucache

import (
	"runtime"
	"sync"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/util/memory"
	"github.com/pkg/errors"
)

// OffHeap is a thread-safe LRU cache of serialized values kept outside the Go heap (util/memory),
// for stores whose decoded values are large and pointer-heavy. Decoded ML-DSA-44 blocks and
// acceptance data cached on the heap were gigabytes of objects the garbage collector had to scan on
// every cycle; as bytes off the heap they are neither scanned nor counted toward GOMEMLIMIT. A hit
// decodes a fresh copy, which costs about what the deep Clone a decoded-value cache must make on
// every hit costs, and hands the caller a value it owns.
//
// Each entry carries meta, a small value kept on the heap next to the bytes, for whatever the
// serialized form does not hold (a block's PoW hash).
//
// The cache is bounded both by entry count and by a byte budget. An entry weighs its size rounded
// up to whole pages, since each buffer is its own OS allocation. Buffers are freed on eviction and
// removal, and the remaining ones when the cache itself becomes unreachable.
type OffHeap[M any] struct {
	lock  sync.Mutex
	state *offHeapState[M]
}

// offHeapState is everything the cleanup needs. It is separate from OffHeap so the cleanup can hold
// it without keeping the OffHeap reachable.
type offHeapState[M any] struct {
	lru        *LRUCache[offHeapEntry[M]]
	capacity   int
	byteBudget int
	bytes      int
}

type offHeapEntry[M any] struct {
	buffer *memory.Block[byte]
	meta   M
}

// offHeapPageSize is the granularity the operating system commits a buffer's memory in.
const offHeapPageSize = 4096

// offHeapWeight is what an entry of size bytes counts against the byte budget.
func offHeapWeight(size int) int {
	return (size + offHeapPageSize - 1) &^ (offHeapPageSize - 1)
}

// NewOffHeap creates an off-heap cache of at most capacity entries weighing at most byteBudget bytes.
func NewOffHeap[M any](capacity, byteBudget int, preallocate bool) *OffHeap[M] {
	c := &OffHeap[M]{state: &offHeapState[M]{
		lru:        New[offHeapEntry[M]](capacity, preallocate),
		capacity:   capacity,
		byteBudget: byteBudget,
	}}
	// The buffers are invisible to the collector, so dropping the cache would leak them. Stores are
	// dropped whole - a consensus after a staging consensus replaces it, every test consensus at
	// teardown - so free what is left once the cache is unreachable.
	runtime.AddCleanup(c, (*offHeapState[M]).freeAll, c.state)
	return c
}

// SizedMarshaler is a message vtprotobuf can marshal into a buffer of exactly its size.
type SizedMarshaler interface {
	SizeVT() int
	MarshalToSizedBufferVT(buffer []byte) (int, error)
}

// MarshalOffHeap serializes message into a buffer outside the Go heap, which the caller owns: it must
// free it, or hand it to an OffHeap with AddBuffer. A store that writes a value to the database and
// then caches it can serialize straight into the buffer the cache keeps, so neither step puts the
// bytes on the heap. Both database backends copy a value into their batch on Put, so the buffer may
// be cached, and later freed, right after it.
func MarshalOffHeap(message SizedMarshaler) (*memory.Block[byte], error) {
	size := message.SizeVT()
	if size == 0 {
		return nil, errors.New("message serialized to zero bytes")
	}
	buffer := memory.Malloc[byte](size)
	written, err := message.MarshalToSizedBufferVT(buffer.Slice())
	if err != nil {
		memory.Free(buffer)
		return nil, err
	}
	if written != size {
		memory.Free(buffer)
		return nil, errors.Errorf("message serialized to %d bytes, expected %d", written, size)
	}
	return buffer, nil
}

// Decode looks key up and, on a hit, calls decode with the entry's bytes and meta while holding the
// cache's lock, so the buffer cannot be freed while decode reads it. decode must copy whatever it
// keeps out of bytes - UnmarshalVT does, UnmarshalVTUnsafe does not. It reports whether key was
// cached; on a hit the error is decode's.
func (c *OffHeap[M]) Decode(key *externalapi.DomainHash, decode func(bytes []byte, meta M) error) (bool, error) {
	c.lock.Lock()
	defer c.lock.Unlock()
	entry, ok := c.state.lru.Get(key)
	if !ok {
		return false, nil
	}
	return true, decode(entry.buffer.Slice(), entry.meta)
}

// Add caches a copy of bytes with meta under key.
func (c *OffHeap[M]) Add(key *externalapi.DomainHash, bytes []byte, meta M) {
	if len(bytes) == 0 || offHeapWeight(len(bytes)) > c.state.byteBudget {
		c.Remove(key)
		return
	}
	buffer := memory.Malloc[byte](len(bytes))
	copy(buffer.Slice(), bytes)
	c.AddBuffer(key, buffer, meta)
}

// AddBuffer caches buffer with meta under key and takes ownership of it: it is freed on eviction or
// removal, or right away if it does not fit the budget.
func (c *OffHeap[M]) AddBuffer(key *externalapi.DomainHash, buffer *memory.Block[byte], meta M) {
	c.lock.Lock()
	defer c.lock.Unlock()
	s := c.state
	s.remove(key)
	weight := offHeapWeight(len(buffer.Slice()))
	if weight > s.byteBudget {
		memory.Free(buffer)
		return
	}
	for s.lru.Len() > 0 && (s.bytes+weight > s.byteBudget || s.lru.Len() >= s.capacity) {
		_, evicted, _ := s.lru.RemoveOldest()
		s.free(evicted)
	}
	s.lru.Add(key, offHeapEntry[M]{buffer: buffer, meta: meta})
	s.bytes += weight
}

// Remove drops key from the cache, if it is there, and frees its buffer.
func (c *OffHeap[M]) Remove(key *externalapi.DomainHash) {
	c.lock.Lock()
	defer c.lock.Unlock()
	c.state.remove(key)
}

// Has reports whether key is cached, without changing LRU order.
func (c *OffHeap[M]) Has(key *externalapi.DomainHash) bool {
	c.lock.Lock()
	defer c.lock.Unlock()
	return c.state.lru.Has(key)
}

// Clear empties the cache and frees every buffer.
func (c *OffHeap[M]) Clear() {
	c.lock.Lock()
	defer c.lock.Unlock()
	c.state.freeAll()
}

// Len returns the number of cached entries.
func (c *OffHeap[M]) Len() int {
	c.lock.Lock()
	defer c.lock.Unlock()
	return c.state.lru.Len()
}

// Bytes returns the total weight of the cached entries.
func (c *OffHeap[M]) Bytes() int {
	c.lock.Lock()
	defer c.lock.Unlock()
	return c.state.bytes
}

func (s *offHeapState[M]) remove(key *externalapi.DomainHash) {
	if entry, ok := s.lru.Peek(key); ok {
		s.lru.Remove(key)
		s.free(entry)
	}
}

func (s *offHeapState[M]) free(entry offHeapEntry[M]) {
	s.bytes -= offHeapWeight(len(entry.buffer.Slice()))
	memory.Free(entry.buffer)
}

// freeAll frees every buffer. It runs as the cleanup of an unreachable OffHeap, when nothing else can
// use the state.
func (s *offHeapState[M]) freeAll() {
	for {
		_, entry, ok := s.lru.RemoveOldest()
		if !ok {
			return
		}
		s.free(entry)
	}
}
