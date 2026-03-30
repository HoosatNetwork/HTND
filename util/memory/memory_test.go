package memory

import (
	"testing"
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
	for i := 0; i < 10; i++ {
		s[i] = i * 2
	}
	for i := 0; i < 10; i++ {
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
	for x := 0; x < 5; x++ {
		b := Malloc[int](1024 * 1024 * 1) // 1 million ints ~ 4MB
		if b == nil {
			t.Fatal("Expected block, got nil")
		}

		s := b.Slice()
		for i := 0; i < 5; i++ {
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

	for i := 0; i < 10; i++ {
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
	for i := 0; i < 5; i++ {
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
	for i := 0; i < 5; i++ {
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
