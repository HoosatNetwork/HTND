package lrucacheghostdagdata

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

func newTestGHOSTDAGData(t *testing.T, score uint64) *externalapi.BlockGHOSTDAGData {
	t.Helper()
	return externalapi.NewBlockGHOSTDAGData(score, big.NewInt(int64(score)), externalapi.NewZeroHash(), nil, nil, nil)
}

func TestLRUCache_AddGetHasRemove_NoEvictionWithinCapacity(t *testing.T) {
	cache := New(10, false)
	blockHash := newTestHash(t, 1)

	dataTrusted := newTestGHOSTDAGData(t, 10)
	dataUntrusted := newTestGHOSTDAGData(t, 20)

	cache.Add(blockHash, true, dataTrusted)
	cache.Add(blockHash, false, dataUntrusted)

	if !cache.Has(blockHash, true) || !cache.Has(blockHash, false) {
		t.Fatalf("expected both entries to exist")
	}

	gotTrusted, ok := cache.Get(blockHash, true)
	if !ok || gotTrusted != dataTrusted {
		t.Fatalf("unexpected trusted get. ok=%v got=%v", ok, gotTrusted)
	}
	gotUntrusted, ok := cache.Get(blockHash, false)
	if !ok || gotUntrusted != dataUntrusted {
		t.Fatalf("unexpected untrusted get. ok=%v got=%v", ok, gotUntrusted)
	}

	cache.Remove(blockHash, true)
	if cache.Has(blockHash, true) {
		t.Fatalf("expected trusted entry removed")
	}
	if cache.Has(blockHash, false) == false {
		t.Fatalf("expected untrusted entry to remain")
	}
}

func TestLRUCache_KeyEqualityByValue(t *testing.T) {
	cache := New(10, true)

	h1 := newTestHash(t, 7)
	h2 := newTestHash(t, 7) // same bytes

	data := newTestGHOSTDAGData(t, 1)
	cache.Add(h1, true, data)

	got, ok := cache.Get(h2, true)
	if !ok || got != data {
		t.Fatalf("expected value-keyed lookup to succeed")
	}
}

func TestLRUCache_OverwriteDoesNotGrow(t *testing.T) {
	cache := New(10, false)
	h := newTestHash(t, 1)

	d1 := newTestGHOSTDAGData(t, 1)
	d2 := newTestGHOSTDAGData(t, 2)

	cache.Add(h, false, d1)
	if got := len(cache.cache); got != 1 {
		t.Fatalf("expected len=1, got %d", got)
	}

	cache.Add(h, false, d2)
	if got := len(cache.cache); got != 1 {
		t.Fatalf("expected len=1 after overwrite, got %d", got)
	}

	got, ok := cache.Get(h, false)
	if !ok || got != d2 {
		t.Fatalf("unexpected overwritten value. ok=%v got=%v", ok, got)
	}
}

func TestLRUCache_EvictsExactlyOneWhenOverCapacity(t *testing.T) {
	cache := New(2, false)

	h1 := newTestHash(t, 1)
	h2 := newTestHash(t, 2)
	h3 := newTestHash(t, 3)

	d1 := newTestGHOSTDAGData(t, 1)
	d2 := newTestGHOSTDAGData(t, 2)
	d3 := newTestGHOSTDAGData(t, 3)

	cache.Add(h1, false, d1)
	cache.Add(h2, false, d2)
	if got := len(cache.cache); got != 2 {
		t.Fatalf("expected len=2, got %d", got)
	}

	cache.Add(h3, false, d3)
	if got := len(cache.cache); got != 2 {
		t.Fatalf("expected len=2 after eviction, got %d", got)
	}

	present := 0
	for _, tc := range []struct {
		h *externalapi.DomainHash
		d *externalapi.BlockGHOSTDAGData
	}{
		{h1, d1},
		{h2, d2},
		{h3, d3},
	} {
		got, ok := cache.Get(tc.h, false)
		if ok {
			present++
			if got != tc.d {
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
		h       byte
		trusted bool
	}
	evicted := make(map[key]int)

	for range trials {
		cache := New(2, false)
		h1 := newTestHash(t, 1)
		h2 := newTestHash(t, 2)
		h3 := newTestHash(t, 3)

		cache.Add(h1, false, newTestGHOSTDAGData(t, 1))
		cache.Add(h2, false, newTestGHOSTDAGData(t, 2))
		cache.Add(h3, true, newTestGHOSTDAGData(t, 3))

		missingCount := 0
		var missing key
		checks := []key{{1, false}, {2, false}, {3, true}}
		for _, k := range checks {
			h := newTestHash(t, k.h)
			if !cache.Has(h, k.trusted) {
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
