package memory

import (
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"unsafe"
)

type Block[T any] struct {
	mem []byte         // keep the original mmap'ed slice
	ptr unsafe.Pointer // derived pointer — only valid while mem exists
	len int            // number of T elements
	id  uint64
}

type allocationInfo struct {
	id       uint64
	typeName string
	length   int
	byteSize int
	ptr      uintptr
}

var (
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
	log.Debugf("malloc id=%d type=%T len=%d bytes=%d ptr=%p", id, *new(T), n, size, ptr)

	return block
}

func (b *Block[T]) Slice() []T {
	if b == nil || b.mem == nil {
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
	copyLen := n
	if b.len < copyLen {
		copyLen = b.len
	}
	copy(newBlock.Slice()[:copyLen], b.Slice()[:copyLen])
	log.Debugf("realloc old_id=%d new_id=%d type=%T old_len=%d new_len=%d old_ptr=%p new_ptr=%p", b.id, newBlock.id, *new(T), b.len, n, b.ptr, newBlock.ptr)
	Free(b)

	return newBlock
}

func Free[T any](b *Block[T]) {
	if b == nil || b.mem == nil {
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
	log.Debugf("free id=%d type=%s len=%d bytes=%d ptr=%p", id, typeName, length, byteSize, ptr)
	*b = Block[T]{} // zero out to prevent use-after-free
}

func LogLeaks() int {
	allocationsMu.Lock()
	defer allocationsMu.Unlock()

	for _, allocation := range allocations {
		log.Warnf("memory block not freed: id=%d type=%s len=%d bytes=%d ptr=%#x", allocation.id, allocation.typeName, allocation.length, allocation.byteSize, allocation.ptr)
	}

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
