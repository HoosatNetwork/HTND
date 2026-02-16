package lrucachehashpairtoblockghostdagdatahashpair

import (
	"math/big"
	"testing"

	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
)

func newTestHash(t *testing.T, b byte) *externalapi.DomainHash {
	t.Helper()
	var arr [externalapi.DomainHashSize]byte
	arr[0] = b
	return externalapi.NewDomainHashFromByteArray(&arr)
}

func newPair(t *testing.T, hashByte byte, score uint64) *externalapi.BlockGHOSTDAGDataHashPair {
	t.Helper()
	h := newTestHash(t, hashByte)
	data := externalapi.NewBlockGHOSTDAGData(score, big.NewInt(int64(score)), externalapi.NewZeroHash(), nil, nil, nil)
	return &externalapi.BlockGHOSTDAGDataHashPair{Hash: h, GHOSTDAGData: data}
}

func TestLRUCache_AddGetHasRemove_NoEvictionWithinCapacity(t *testing.T) {
	cache := New(10, false)

	blockHash := newTestHash(t, 1)
	p1 := newPair(t, 10, 1)
	p2 := newPair(t, 11, 2)

	cache.Add(blockHash, 0, p1)
	cache.Add(blockHash, 1, p2)

	if !cache.Has(blockHash, 0) || !cache.Has(blockHash, 1) {
		t.Fatalf("expected keys to exist")
	}

	got1, ok := cache.Get(blockHash, 0)
	if !ok || got1 != p1 {
		t.Fatalf("unexpected get for index 0")
	}
	got2, ok := cache.Get(blockHash, 1)
	if !ok || got2 != p2 {
		t.Fatalf("unexpected get for index 1")
	}

	cache.Remove(blockHash, 0)
	if cache.Has(blockHash, 0) {
		t.Fatalf("expected removed")
	}
	if cache.Has(blockHash, 1) == false {
		t.Fatalf("expected other entry to remain")
	}
}

func TestLRUCache_KeyEqualityByValue(t *testing.T) {
	cache := New(10, true)

	h1 := newTestHash(t, 7)
	h2 := newTestHash(t, 7) // same bytes
	p := newPair(t, 10, 1)

	cache.Add(h1, 5, p)
	got, ok := cache.Get(h2, 5)
	if !ok || got != p {
		t.Fatalf("expected value-keyed lookup to succeed")
	}
}

func TestLRUCache_OverwriteDoesNotGrow(t *testing.T) {
	cache := New(10, false)

	h := newTestHash(t, 1)
	p1 := newPair(t, 10, 1)
	p2 := newPair(t, 11, 2)

	cache.Add(h, 1, p1)
	if got := len(cache.cache); got != 1 {
		t.Fatalf("expected len=1, got %d", got)
	}

	cache.Add(h, 1, p2)
	if got := len(cache.cache); got != 1 {
		t.Fatalf("expected len=1 after overwrite, got %d", got)
	}

	got, ok := cache.Get(h, 1)
	if !ok || got != p2 {
		t.Fatalf("unexpected overwritten value")
	}
}

func TestLRUCache_EvictsExactlyOneWhenOverCapacity(t *testing.T) {
	cache := New(2, false)

	h1 := newTestHash(t, 1)
	h2 := newTestHash(t, 2)
	h3 := newTestHash(t, 3)

	p1 := newPair(t, 10, 1)
	p2 := newPair(t, 11, 2)
	p3 := newPair(t, 12, 3)

	cache.Add(h1, 0, p1)
	cache.Add(h2, 0, p2)
	if got := len(cache.cache); got != 2 {
		t.Fatalf("expected len=2, got %d", got)
	}

	cache.Add(h3, 0, p3)
	if got := len(cache.cache); got != 2 {
		t.Fatalf("expected len=2 after eviction, got %d", got)
	}

	present := 0
	for _, tc := range []struct {
		h *externalapi.DomainHash
		p *externalapi.BlockGHOSTDAGDataHashPair
	}{
		{h1, p1},
		{h2, p2},
		{h3, p3},
	} {
		got, ok := cache.Get(tc.h, 0)
		if ok {
			present++
			if got != tc.p {
				t.Fatalf("wrong value returned")
			}
		}
	}
	if present != 2 {
		t.Fatalf("expected exactly 2 keys to remain, got %d", present)
	}
}

func TestLRUCache_RandomEvictionVariesAcrossTrials(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping probabilistic eviction test in -short")
	}

	const trials = 200
	type key struct {
		h byte
		i uint64
	}
	evicted := make(map[key]int)

	for range trials {
		cache := New(2, false)
		h1 := newTestHash(t, 1)
		h2 := newTestHash(t, 2)
		h3 := newTestHash(t, 3)

		cache.Add(h1, 0, newPair(t, 10, 1))
		cache.Add(h2, 0, newPair(t, 11, 2))
		cache.Add(h3, 1, newPair(t, 12, 3))

		missingCount := 0
		var missing key
		checks := []key{{1, 0}, {2, 0}, {3, 1}}
		for _, k := range checks {
			h := newTestHash(t, k.h)
			if !cache.Has(h, k.i) {
				missing = k
				missingCount++
			}
		}
		if missingCount != 1 {
			t.Fatalf("expected exactly 1 evicted entry, got %d", missingCount)
		}
		evicted[missing]++
	}

	if len(evicted) < 2 {
		t.Fatalf("expected eviction to vary across trials, got evicted set: %v", evicted)
	}
}
