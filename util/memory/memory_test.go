package memory

import (
	"testing"
)

func TestMallocAndFree(t *testing.T) {
	b := Malloc[int](10)
	if b == nil {
		t.Fatal("Expected block, got nil")
	}
	defer Free(b)

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
