package memory

import (
	"fmt"
	"os"
	"reflect"
	"sync"
	"sync/atomic"
	"unsafe"
)

type Block[T any] struct {
	mem []byte         // keep the original mmap'ed slice
	ptr unsafe.Pointer // derived pointer — only valid while mem exists
	len int            // number of T elements
	id  uint64

	// managed backs the block with an ordinary Go slice instead of mmap'ed memory, for element
	// types that contain pointers. mem is nil in that case and ptr is unused.
	//
	// This is not an optimisation choice, it is a correctness one. The mmap'ed region is invisible
	// to the garbage collector: it is neither a GC root nor scanned for references. Storing a Go
	// pointer there means the object it points at has no reference the collector can see, so the
	// collector is free to reclaim it while the block still "holds" it. Reading the block later
	// then yields a pointer into reclaimed memory.
	//
	// That is not theoretical. A node crashed with a nil pointer dereference deep inside
	// DbUtxoDiff.MarshalVT, at a line guarded by an explicit nil check one frame up - the classic
	// signature of an object that was valid when it was stored and was collected before it was
	// read. The buffer held []*DbUtxoCollectionItem: pointers, in unscanned memory.
	//
	// Pointer-free element types (plain numeric structs) stay on mmap, where none of this applies.
	managed []T
}

// pointerful caches, per element type, whether values of that type contain anything the garbage
// collector must be able to see. Strings, slices, maps, channels, funcs and interfaces all carry
// pointers, so they count too - not just declared pointer types.
var pointerful sync.Map // reflect.Type -> bool

func containsPointers[T any]() bool {
	typ := reflect.TypeOf((*T)(nil)).Elem()
	if cached, ok := pointerful.Load(typ); ok {
		return cached.(bool)
	}
	result := typeContainsPointers(typ)
	pointerful.Store(typ, result)
	return result
}

func typeContainsPointers(typ reflect.Type) bool {
	switch typ.Kind() {
	case reflect.Pointer, reflect.UnsafePointer, reflect.Chan, reflect.Map, reflect.Func,
		reflect.Slice, reflect.String, reflect.Interface:
		return true
	case reflect.Array:
		return typeContainsPointers(typ.Elem())
	case reflect.Struct:
		for i := 0; i < typ.NumField(); i++ {
			if typeContainsPointers(typ.Field(i).Type) {
				return true
			}
		}
		return false
	default:
		return false
	}
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

	if containsPointers[T]() {
		// Go-managed, so the collector can see the pointers this block will hold. See Block.managed.
		id := atomic.AddUint64(&allocationSeq, 1)
		block := &Block[T]{managed: make([]T, n), len: n, id: id}
		log.Debugf("Malloc (Go-managed, element type contains pointers) id=%d type=%T len=%d", id, *new(T), n)
		return block
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
	if b.managed != nil {
		return b.managed
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
	if b == nil || (b.mem == nil && b.managed == nil) {
		return Malloc[T](n)
	}
	if n == b.len {
		return b
	}

	newBlock := Malloc[T](n)
	copyLen := min(b.len, n)
	copy(newBlock.Slice()[:copyLen], b.Slice()[:copyLen])
	log.Debugf("Realloc old_id=%d new_id=%d type=%T old_len=%d new_len=%d old_ptr=%p new_ptr=%p", b.id, newBlock.id, *new(T), b.len, n, b.ptr, newBlock.ptr)
	Free(b)

	return newBlock
}

func Free[T any](b *Block[T]) {
	if b == nil {
		return
	}
	if b.managed != nil {
		// Nothing to unmap: dropping the reference is what frees it, and the collector does the
		// rest. Zeroing still matters, so a use-after-free reads as empty rather than as live data.
		log.Debugf("Free (Go-managed) id=%d type=%T len=%d", b.id, *new(T), b.len)
		*b = Block[T]{}
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
	*b = Block[T]{} // zero out to prevent use-after-free
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
