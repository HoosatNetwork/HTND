package memory

import (
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"unsafe"
)

// Block is n elements of T in mmap'ed memory the garbage collector neither manages nor scans.
//
// That last part is the catch for element types that hold Go pointers (pointers, interfaces,
// slices, strings, maps). A pointer stored in the block is invisible to the collector, so if the
// block holds the only reference to a Go heap object, the collector frees that object while the
// block still points at it. Copying the stale pointer back out later crashes the process with
// "found bad pointer in Go heap", or silently reads another object's memory.
//
// Whoever stores a Go heap pointer in a block must therefore also hand it to Retain, which keeps
// the object alive until the block is freed. Pointers to memory that is not Go heap (another
// block, for instance) need nothing.
type Block[T any] struct {
	mem []byte         // keep the original mmap'ed slice
	ptr unsafe.Pointer // derived pointer — only valid while mem exists
	len int            // number of T elements
	id  uint64

	// retained holds, where the collector can see them, the Go heap objects this block's elements
	// point at. See Retain.
	retained *retainChunk
}

// retainChunkLen makes a retainChunk exactly 8 KiB: small fixed-size allocations instead of one
// slice that doubles, so retaining never copies and never asks the heap for a huge object.
const retainChunkLen = 511

type retainChunk struct {
	next *retainChunk
	n    int
	refs [retainChunkLen]any
}

// Retain keeps the Go heap object v refers to alive until b is freed. Call it for every Go heap
// pointer written into b's elements, before the last ordinary reference to the object goes away -
// in practice, right before the write. v is the pointer itself, or the interface value holding it.
//
// It costs 16 bytes of Go heap per call and nothing for blocks that never call it, which is why
// retention is explicit instead of moving every pointer-holding block onto the Go heap.
//
// A nil or freed block retains nothing: there is no off-heap memory for v to be hidden in.
func (b *Block[T]) Retain(v any) {
	if b == nil || b.mem == nil || v == nil {
		return
	}
	chunk := b.retained
	if chunk == nil || chunk.n == retainChunkLen {
		chunk = &retainChunk{next: b.retained}
		b.retained = chunk
	}
	chunk.refs[chunk.n] = v
	chunk.n++
}

type allocationInfo struct {
	id       uint64
	typeName string
	length   int
	byteSize int
	ptr      uintptr
}

var (
	logLeaks      uint64
	allocationSeq uint64
	allocationsMu sync.Mutex
	allocations   = make(map[uint64]allocationInfo)
)

func Malloc[T any](n int) *Block[T] {
	if n <= 0 {
		return nil
	}

	elemSize := int(unsafe.Sizeof(*new(T)))
	size := elemSize * n

	mem, err := sysAlloc(size)
	if err != nil {
		panic(err)
	}

	// Important: use unsafe.Slice to create typed view without copying
	ptr := unsafe.Pointer(unsafe.SliceData(mem))
	id := atomic.AddUint64(&allocationSeq, 1)

	block := &Block[T]{
		mem: mem,
		ptr: ptr,
		len: n,
		id:  id,
	}

	registerAllocation(block, size)
	log.Debugf("Malloc id=%d type=%T len=%d bytes=%d ptr=%p", id, *new(T), n, size, ptr)

	return block
}

func (b *Block[T]) Slice() []T {
	if b == nil {
		return nil
	}
	if b.mem == nil {
		return nil
	}
	// Re-derive slice every time — safest
	return unsafe.Slice((*T)(b.ptr), b.len)
}

func Calloc[T any](n int) *Block[T] {
	b := Malloc[T](n)
	if b != nil {
		clear(b.Slice())
	}
	return b
}

func Realloc[T any](b *Block[T], n int) *Block[T] {
	if n <= 0 {
		Free(b)
		return nil
	}
	if b == nil || b.mem == nil {
		return Malloc[T](n)
	}
	if n == b.len {
		return b
	}

	newBlock := Malloc[T](n)
	copyLen := min(b.len, n)
	copy(newBlock.Slice()[:copyLen], b.Slice()[:copyLen])
	// The copied elements still point at whatever b retained for them.
	newBlock.retained = b.retained
	log.Debugf("Realloc old_id=%d new_id=%d type=%T old_len=%d new_len=%d old_ptr=%p new_ptr=%p", b.id, newBlock.id, *new(T), b.len, n, b.ptr, newBlock.ptr)
	Free(b)

	return newBlock
}

func Free[T any](b *Block[T]) {
	if b == nil {
		return
	}
	if b.mem == nil {
		return
	}
	ptr := b.ptr
	id := b.id
	length := b.len
	byteSize := len(b.mem)
	typeName := fmt.Sprintf("%T", *new(T))
	err := sysFree(b.mem)
	if err != nil {
		panic(err)
	}
	unregisterAllocation(id)
	log.Debugf("Free id=%d type=%s len=%d bytes=%d ptr=%p", id, typeName, length, byteSize, ptr)
	*b = Block[T]{} // zero out to prevent use-after-free; this also lets go of everything retained
}

func LogLeaks() int {
	if os.Getenv("MEMORY_ALLOCATIONS") == "" {
		return 0
	}
	allocationsMu.Lock()
	defer allocationsMu.Unlock()

	for _, allocation := range allocations {
		log.Infof("Memory block not freed yet on logLeaks %d: id=%d type=%s len=%d bytes=%d ptr=%#x", logLeaks, allocation.id, allocation.typeName, allocation.length, allocation.byteSize, allocation.ptr)
	}
	logLeaks++
	return len(allocations)
}

func registerAllocation[T any](b *Block[T], byteSize int) {
	if os.Getenv("MEMORY_ALLOCATIONS") == "" {
		return
	}
	allocationsMu.Lock()
	defer allocationsMu.Unlock()

	allocations[b.id] = allocationInfo{
		id:       b.id,
		typeName: fmt.Sprintf("%T", *new(T)),
		length:   b.len,
		byteSize: byteSize,
		ptr:      uintptr(b.ptr),
	}
}

func unregisterAllocation(id uint64) {
	if os.Getenv("MEMORY_ALLOCATIONS") == "" {
		return
	}
	allocationsMu.Lock()
	defer allocationsMu.Unlock()

	delete(allocations, id)
}

func outstandingAllocationsCount() int {
	if os.Getenv("MEMORY_ALLOCATIONS") == "" {
		return 0
	}
	allocationsMu.Lock()
	defer allocationsMu.Unlock()

	return len(allocations)
}
