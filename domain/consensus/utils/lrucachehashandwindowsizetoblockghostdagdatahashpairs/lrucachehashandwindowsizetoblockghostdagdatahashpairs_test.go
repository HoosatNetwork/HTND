package lrucachehashandwindowsizetoblockghostdagdatahashpairs

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
	windowSize := 100
	p1 := newPair(t, 10, 1)
	p2 := newPair(t, 11, 2)
	value := []*externalapi.BlockGHOSTDAGDataHashPair{p1, p2}

	cache.Add(blockHash, windowSize, value)

	if !cache.Has(blockHash, windowSize) {
		t.Fatalf("expected entry to exist")
	}

	got, ok := cache.Get(blockHash, windowSize)
	if !ok {
		t.Fatalf("expected get to succeed")
	}
	if len(got) != 2 || got[0] != p1 || got[1] != p2 {
		t.Fatalf("unexpected returned slice")
	}

	cache.Remove(blockHash, windowSize)
	if cache.Has(blockHash, windowSize) {
		t.Fatalf("expected removed")
	}
}

func TestLRUCache_DistinguishesWindowSize(t *testing.T) {
	cache := New(10, true)
	blockHash := newTestHash(t, 1)

	p1 := newPair(t, 10, 1)
	p2 := newPair(t, 11, 2)

	cache.Add(blockHash, 1, []*externalapi.BlockGHOSTDAGDataHashPair{p1})
	cache.Add(blockHash, 2, []*externalapi.BlockGHOSTDAGDataHashPair{p2})

	got1, ok := cache.Get(blockHash, 1)
	if !ok || len(got1) != 1 || got1[0] != p1 {
		t.Fatalf("unexpected get for windowSize=1")
	}
	got2, ok := cache.Get(blockHash, 2)
	if !ok || len(got2) != 1 || got2[0] != p2 {
		t.Fatalf("unexpected get for windowSize=2")
	}
}

func TestLRUCache_KeyEqualityByValue(t *testing.T) {
	cache := New(10, false)

	h1 := newTestHash(t, 7)
	h2 := newTestHash(t, 7) // same bytes

	p := newPair(t, 10, 1)
	cache.Add(h1, 5, []*externalapi.BlockGHOSTDAGDataHashPair{p})

	got, ok := cache.Get(h2, 5)
	if !ok || len(got) != 1 || got[0] != p {
		t.Fatalf("expected value-keyed lookup to succeed")
	}
}

func TestLRUCache_OverwriteDoesNotGrow(t *testing.T) {
	cache := New(10, false)

	h := newTestHash(t, 1)
	p1 := newPair(t, 10, 1)
	p2 := newPair(t, 11, 2)

	cache.Add(h, 1, []*externalapi.BlockGHOSTDAGDataHashPair{p1})
	if got := len(cache.cache); got != 1 {
		t.Fatalf("expected len=1, got %d", got)
	}

	cache.Add(h, 1, []*externalapi.BlockGHOSTDAGDataHashPair{p2})
	if got := len(cache.cache); got != 1 {
		t.Fatalf("expected len=1 after overwrite, got %d", got)
	}

	got, ok := cache.Get(h, 1)
	if !ok || len(got) != 1 || got[0] != p2 {
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

	cache.Add(h1, 1, []*externalapi.BlockGHOSTDAGDataHashPair{p1})
	cache.Add(h2, 1, []*externalapi.BlockGHOSTDAGDataHashPair{p2})
	if got := len(cache.cache); got != 2 {
		t.Fatalf("expected len=2, got %d", got)
	}

	cache.Add(h3, 1, []*externalapi.BlockGHOSTDAGDataHashPair{p3})
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
		got, ok := cache.Get(tc.h, 1)
		if ok {
			present++
			if len(got) != 1 || got[0] != tc.p {
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
		w int
	}
	evicted := make(map[key]int)

	for range trials {
		cache := New(2, false)
		h1 := newTestHash(t, 1)
		h2 := newTestHash(t, 2)
		h3 := newTestHash(t, 3)

		cache.Add(h1, 1, []*externalapi.BlockGHOSTDAGDataHashPair{newPair(t, 10, 1)})
		cache.Add(h2, 1, []*externalapi.BlockGHOSTDAGDataHashPair{newPair(t, 11, 2)})
		cache.Add(h3, 2, []*externalapi.BlockGHOSTDAGDataHashPair{newPair(t, 12, 3)})

		missingCount := 0
		var missing key
		checks := []key{{1, 1}, {2, 1}, {3, 2}}
		for _, k := range checks {
			h := newTestHash(t, k.h)
			if !cache.Has(h, k.w) {
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
