package lrucache

import (
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
)

func newTestHash(t *testing.T, b byte) *externalapi.DomainHash {
	t.Helper()
	var arr [externalapi.DomainHashSize]byte
	arr[0] = b
	return externalapi.NewDomainHashFromByteArray(&arr)
}

func TestLRUCache_AddGetHasRemove_NoEvictionWithinCapacity(t *testing.T) {
	cache := New[any](10, false)

	key1 := newTestHash(t, 1)
	key2 := newTestHash(t, 2)

	cache.Add(key1, "v1")
	cache.Add(key2, 123)

	if !cache.Has(key1) || !cache.Has(key2) {
		t.Fatalf("expected keys to exist")
	}

	v1, ok := cache.Get(key1)
	if !ok || v1.(string) != "v1" {
		t.Fatalf("unexpected get for key1. ok=%v v=%v", ok, v1)
	}
	v2, ok := cache.Get(key2)
	if !ok || v2.(int) != 123 {
		t.Fatalf("unexpected get for key2. ok=%v v=%v", ok, v2)
	}

	cache.Remove(key1)
	if cache.Has(key1) {
		t.Fatalf("expected key1 to be removed")
	}
	if _, ok := cache.Get(key1); ok {
		t.Fatalf("expected key1 get to fail after remove")
	}
}

func TestLRUCache_KeyEqualityByValue(t *testing.T) {
	cache := New[any](10, true)

	keyA1 := newTestHash(t, 7)
	keyA2 := newTestHash(t, 7) // same bytes, different pointer

	cache.Add(keyA1, "value")
	v, ok := cache.Get(keyA2)
	if !ok {
		t.Fatalf("expected value-keyed lookup to succeed")
	}
	if v.(string) != "value" {
		t.Fatalf("unexpected value: %v", v)
	}
}

func TestLRUCache_OverwriteDoesNotGrow(t *testing.T) {
	cache := New[any](10, false)
	key := newTestHash(t, 3)

	cache.Add(key, "first")
	if got := len(cache.cache); got != 1 {
		t.Fatalf("expected len=1, got %d", got)
	}

	cache.Add(key, "second")
	if got := len(cache.cache); got != 1 {
		t.Fatalf("expected len=1 after overwrite, got %d", got)
	}

	v, ok := cache.Get(key)
	if !ok || v.(string) != "second" {
		t.Fatalf("unexpected overwritten value. ok=%v v=%v", ok, v)
	}
}

func TestLRUCache_EvictsExactlyOneWhenOverCapacity(t *testing.T) {
	cache := New[any](2, false)

	key1 := newTestHash(t, 1)
	key2 := newTestHash(t, 2)
	key3 := newTestHash(t, 3)

	cache.Add(key1, "v1")
	cache.Add(key2, "v2")
	if got := len(cache.cache); got != 2 {
		t.Fatalf("expected len=2, got %d", got)
	}

	cache.Add(key3, "v3")
	if got := len(cache.cache); got != 2 {
		t.Fatalf("expected len=2 after eviction, got %d", got)
	}

	present := 0
	for _, tc := range []struct {
		k *externalapi.DomainHash
		v string
	}{
		{key1, "v1"},
		{key2, "v2"},
		{key3, "v3"},
	} {
		got, ok := cache.Get(tc.k)
		if ok {
			present++
			if got.(string) != tc.v {
				t.Fatalf("key %s returned wrong value. want=%s got=%v", tc.k, tc.v, got)
			}
		}
	}

	if present != 2 {
		t.Fatalf("expected exactly 2 keys to remain, got %d", present)
	}
}

func TestLRUCache_RandomEvictionVariesAcrossTrials(t *testing.T) {
	// Eviction is deterministic LRU (least recently used).
	cache := New[string](2, false)

	key1 := newTestHash(t, 1)
	key2 := newTestHash(t, 2)
	key3 := newTestHash(t, 3)

	cache.Add(key1, "v1")
	cache.Add(key2, "v2")

	// Touch key1 so that key2 becomes the LRU.
	if _, ok := cache.Get(key1); !ok {
		t.Fatalf("expected key1 to exist")
	}

	// Adding key3 should evict key2 (the LRU).
	cache.Add(key3, "v3")

	if cache.Has(key2) {
		t.Fatalf("expected key2 to be evicted as LRU")
	}
	if !cache.Has(key1) || !cache.Has(key3) {
		t.Fatalf("expected key1 and key3 to remain")
	}
}

// func TestLRUCache_ConcurrentAccess_DoesNotPanic(t *testing.T) {
// 	cache := New[int](128, true)

// 	keys := make([]*externalapi.DomainHash, 512)
// 	for i := range keys {
// 		keys[i] = newTestHash(t, byte(i))
// 	}

// 	stop := &atomic.Bool{}
// 	var wg sync.WaitGroup

// 	workers := max(runtime.NumCPU()*4, 8)

// 	// Writers
// 	for w := 0; w < workers/2; w++ {
// 		wg.Add(1)
// 		go func(workerID int) {
// 			defer wg.Done()
// 			for i := 0; i < 50_000 && !stop.Load(); i++ {
// 				k := keys[(i+workerID)%len(keys)]
// 				cache.Add(k, i)
// 				if i%17 == 0 {
// 					cache.Remove(k)
// 				}
// 			}
// 		}(w)
// 	}

// 	// Readers
// 	for r := 0; r < workers/2; r++ {
// 		wg.Add(1)
// 		go func(workerID int) {
// 			defer wg.Done()
// 			for i := 0; i < 50_000 && !stop.Load(); i++ {
// 				k := keys[(i*31+workerID)%len(keys)]
// 				cache.Has(k)
// 				cache.Get(k)
// 				if i%101 == 0 {
// 					cache.Len()
// 				}
// 			}
// 		}(r)
// 	}

// 	wg.Wait()
// }

func TestLRUCache_NilKey_DoesNotPanic(t *testing.T) {
	cache := New[string](10, false)

	// These should not panic
	cache.Add(nil, "value")
	v, ok := cache.Get(nil)
	if ok || v != "" {
		t.Fatalf("expected Get(nil) to return false, got ok=%v v=%v", ok, v)
	}
	if cache.Has(nil) {
		t.Fatalf("expected Has(nil) to return false")
	}
	cache.Remove(nil)
}
