package lrucacheuint64tohash

import (
	"testing"

	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
)

func newTestHash(t *testing.T, b byte) *externalapi.DomainHash {
	t.Helper()
	var arr [externalapi.DomainHashSize]byte
	arr[0] = b
	return externalapi.NewDomainHashFromByteArray(&arr)
}

func TestLRUCache_AddGetHasRemove_NoEvictionWithinCapacity(t *testing.T) {
	cache := New(10, false)

	h1 := newTestHash(t, 1)
	h2 := newTestHash(t, 2)

	cache.Add(10, h1)
	cache.Add(20, h2)

	if !cache.Has(10) || !cache.Has(20) {
		t.Fatalf("expected keys to exist")
	}

	got1, ok := cache.Get(10)
	if !ok || got1 != h1 {
		t.Fatalf("unexpected get for 10. ok=%v got=%v", ok, got1)
	}
	got2, ok := cache.Get(20)
	if !ok || got2 != h2 {
		t.Fatalf("unexpected get for 20. ok=%v got=%v", ok, got2)
	}

	cache.Remove(10)
	if cache.Has(10) {
		t.Fatalf("expected key 10 removed")
	}
	if _, ok := cache.Get(10); ok {
		t.Fatalf("expected get to fail after remove")
	}
}

func TestLRUCache_OverwriteDoesNotGrow(t *testing.T) {
	cache := New(10, true)

	h1 := newTestHash(t, 1)
	h2 := newTestHash(t, 2)

	cache.Add(5, h1)
	if got := len(cache.cache); got != 1 {
		t.Fatalf("expected len=1, got %d", got)
	}

	cache.Add(5, h2)
	if got := len(cache.cache); got != 1 {
		t.Fatalf("expected len=1 after overwrite, got %d", got)
	}

	got, ok := cache.Get(5)
	if !ok || got != h2 {
		t.Fatalf("unexpected overwritten value. ok=%v got=%v", ok, got)
	}
}

func TestLRUCache_EvictsExactlyOneWhenOverCapacity(t *testing.T) {
	cache := New(2, false)

	h1 := newTestHash(t, 1)
	h2 := newTestHash(t, 2)
	h3 := newTestHash(t, 3)

	cache.Add(1, h1)
	cache.Add(2, h2)
	if got := len(cache.cache); got != 2 {
		t.Fatalf("expected len=2, got %d", got)
	}

	cache.Add(3, h3)
	if got := len(cache.cache); got != 2 {
		t.Fatalf("expected len=2 after eviction, got %d", got)
	}

	present := 0
	for key, want := range map[uint64]*externalapi.DomainHash{1: h1, 2: h2, 3: h3} {
		got, ok := cache.Get(key)
		if ok {
			present++
			if got != want {
				t.Fatalf("key %d returned wrong value", key)
			}
		}
	}
	if present != 2 {
		t.Fatalf("expected exactly 2 keys to remain, got %d", present)
	}
}
