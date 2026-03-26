package memory

import (
	"unsafe"
)

type Block[T any] struct {
	mem []byte         // keep the original mmap'ed slice
	ptr unsafe.Pointer // derived pointer — only valid while mem exists
	len int            // number of T elements
}

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

	return &Block[T]{
		mem: mem,
		ptr: ptr,
		len: n,
	}
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
	Free(b)

	return newBlock
}

func Free[T any](b *Block[T]) {
	if b == nil || b.mem == nil {
		return
	}
	err := sysFree(b.mem)
	if err != nil {
		panic(err)
	}
	*b = Block[T]{} // zero out to prevent use-after-free
}
