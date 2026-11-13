package memory

import (
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

func TestMallocAndFree(t *testing.T) {
	b := Malloc[int](10)
	if b == nil {
		t.Fatal("Expected block, got nil")
	}

	s := b.Slice()
	if len(s) != 10 {
		t.Fatalf("Expected length 10, got %d", len(s))
	}

	// Write to it
	for i := range 10 {
		s[i] = i * 2
	}
	for i := range 10 {
		if s[i] != i*2 {
			t.Fatalf("Expected %d, got %d at index %d", i*2, s[i], i)
		}
	}
	Free(b)
	s2 := b.Slice()
	if len(s2) != 0 {
		t.Fatalf("Expected length 0 after free, got %d", len(s2))
	}
}

func TestMallocReallocAndFree(t *testing.T) {
	for range 5 {
		b := Malloc[int](1024 * 1024 * 1) // 1 million ints ~ 4MB
		if b == nil {
			t.Fatal("Expected block, got nil")
		}

		s := b.Slice()
		for i := range 5 {
			s[i] = i + 1
		}
		b = Realloc(b, 1024*1024*2) // 2 million ints ~ 8MB
		s = b.Slice()
		if b == nil {
			t.Fatal("Expected valid block after Realloc")
		}
		for i := 5; i > 0; i-- {
			s[i] = i
		}
		Free(b)
		s = b.Slice()
		if len(s) != 0 {
			t.Fatalf("Expected length 0 after free, got %d", len(s))
		}
	}
}

func TestCalloc(t *testing.T) {
	b := Calloc[int](10)
	if b == nil {
		t.Fatal("Expected block, got nil")
	}
	defer Free(b)

	s := b.Slice()
	if len(s) != 10 {
		t.Fatalf("Expected length 10, got %d", len(s))
	}

	for i := range 10 {
		if s[i] != 0 {
			t.Fatalf("Expected 0 from Calloc, got %d at index %d", s[i], i)
		}
	}
}

func TestRealloc(t *testing.T) {
	b := Malloc[int](5)
	if b == nil {
		t.Fatal("Expected block, got nil")
	}
	s := b.Slice()
	for i := range 5 {
		s[i] = i + 1
	}

	b = Realloc(b, 10)
	if b == nil {
		t.Fatal("Expected valid block after Realloc")
	}
	defer Free(b)

	s = b.Slice()
	if len(s) != 10 {
		t.Fatalf("Expected length 10, got %d", len(s))
	}
	for i := range 5 {
		if s[i] != i+1 {
			t.Fatalf("Expected %d, got %d at index %d", i+1, s[i], i)
		}
	}
}

func BenchmarkMalloc(b *testing.B) {
	for i := 0; i < b.N; i++ {
		blk := Malloc[int](1024 * 1024 * 256) // 256 million ints ~ 1GB
		Free(blk)
	}
}

func BenchmarkGolangSlice(b *testing.B) {
	for i := 0; i < b.N; i++ {
		s := make([]int, 1024*1024*256)
		clear(s)
	}
}

func BenchmarkCalloc(b *testing.B) {
	for i := 0; i < b.N; i++ {
		blk := Calloc[int](1024 * 1024 * 256) // 256 million ints ~ 1GB
		Free(blk)
	}
}

func BenchmarkRealloc(b *testing.B) {
	b.StopTimer()
	blk := Malloc[int](1024 * 1024 * 256) // 256 million ints ~ 1GB
	b.StartTimer()
	for i := 0; i < b.N; i++ {
		blk = Realloc(blk, 1024*1024*512)
		blk = Realloc(blk, 1024*1024*256)
	}
	b.StopTimer()
	Free(blk)
}

func TestLogLeaksTracksOutstandingAllocations(t *testing.T) {
	t.Setenv("MEMORY_ALLOCATIONS", "1")

	b := Malloc[int](4)
	if b == nil {
		t.Fatal("Expected block, got nil")
	}

	if count := outstandingAllocationsCount(); count != 1 {
		t.Fatalf("Expected 1 outstanding allocation, got %d", count)
	}

	if count := LogLeaks(); count != 1 {
		t.Fatalf("Expected LogLeaks to report 1 outstanding allocation, got %d", count)
	}

	Free(b)

	if count := LogLeaks(); count != 0 {
		t.Fatalf("Expected LogLeaks to report 0 outstanding allocations after Free, got %d", count)
	}
}

type retainedObject struct{ value [64]byte }

// A block is not scanned by the garbage collector, so an object referenced only from a block is
// collected under it unless the block retains it. This pins both halves: a retained object
// survives collections for as long as the block lives, and Free lets it go.
func TestRetainKeepsObjectsAliveUntilFree(t *testing.T) {
	const count = 2000 // several retain chunks

	var finalized atomic.Int64
	// Starts too small, so Realloc has to carry what was retained over to the new block.
	b := fillRetained(Malloc[*retainedObject](1), count, &finalized)

	for range 3 {
		runtime.GC()
	}
	if got := finalized.Load(); got != 0 {
		t.Fatalf("%d of %d retained objects were collected while the block was alive", got, count)
	}
	for i, object := range b.Slice() {
		if object.value[0] != byte(i) {
			t.Fatalf("object %d reads %d, want %d", i, object.value[0], byte(i))
		}
	}

	Free(b)
	deadline := time.Now().Add(10 * time.Second)
	for finalized.Load() != count && time.Now().Before(deadline) {
		runtime.GC()
		time.Sleep(10 * time.Millisecond)
	}
	if got := finalized.Load(); got != count {
		t.Fatalf("%d of %d objects were collected after Free; the block still holds the rest", got, count)
	}
}

// fillRetained is its own function so no object stays referenced from the test's stack frame.
//
//go:noinline
func fillRetained(b *Block[*retainedObject], count int, finalized *atomic.Int64) *Block[*retainedObject] {
	b.Retain(&retainedObject{}) // retained before the block grows
	b = Realloc(b, count)
	s := b.Slice()
	for i := range s {
		object := &retainedObject{}
		object.value[0] = byte(i)
		runtime.SetFinalizer(object, func(*retainedObject) { finalized.Add(1) })
		b.Retain(object)
		s[i] = object
	}
	return b
}

func TestRetainOnNilOrFreedBlockIsANoOp(t *testing.T) {
	var nilBlock *Block[*int]
	nilBlock.Retain(new(int))

	b := Malloc[*int](1)
	Free(b)
	b.Retain(new(int))
	if b.retained != nil {
		t.Fatal("a freed block retained an object")
	}
}
