package lrucache

import (
	"bytes"
	"runtime"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/util/memory"
)

// TestOffHeapStaysWithinBudgetAndAccountsWeight pins that the cache evicts least-recently-used
// entries until both its count and byte budget hold, that its running weight matches what it holds,
// and that hits return the stored bytes and meta.
func TestOffHeapStaysWithinBudgetAndAccountsWeight(t *testing.T) {
	// Every 3,700-byte entry weighs one 4 KB page, so a 10 KB budget holds two although the count
	// limit would allow ten.
	c := NewOffHeap[string](10, 10_000, false)
	value := func(i int) []byte { return bytes.Repeat([]byte{byte(i)}, 3700) }
	metas := []string{"a", "b", "c", "d", "e", "f"}

	for i := range 6 {
		c.Add(newTestHash(t, byte(i)), value(i), metas[i])
		if c.Bytes() > 10_000 || c.Bytes() != c.Len()*offHeapPageSize {
			t.Fatalf("after %d adds: %d entries weigh %d bytes", i+1, c.Len(), c.Bytes())
		}
	}
	if c.Len() != 2 {
		t.Fatalf("cache holds %d entries, want 2", c.Len())
	}
	for i := range 4 {
		if c.Has(newTestHash(t, byte(i))) {
			t.Fatalf("entry %d should have been evicted", i)
		}
	}

	hit, err := c.Decode(newTestHash(t, 5), func(got []byte, meta string) error {
		if !bytes.Equal(got, value(5)) || meta != "f" {
			t.Fatalf("hit returned the wrong bytes or meta %q", meta)
		}
		return nil
	})
	if !hit || err != nil {
		t.Fatalf("Decode(5) = %v, %v", hit, err)
	}
	if hit, _ := c.Decode(newTestHash(t, 0), func([]byte, string) error { return nil }); hit {
		t.Fatalf("an evicted entry was a hit")
	}

	// Re-adding a key replaces it without double-counting, and an entry over the budget is dropped
	// along with the old value under its key.
	c.Add(newTestHash(t, 5), value(5), "f2")
	if c.Len() != 2 || c.Bytes() != 2*offHeapPageSize {
		t.Fatalf("re-add: %d entries weigh %d bytes", c.Len(), c.Bytes())
	}
	c.Add(newTestHash(t, 5), bytes.Repeat([]byte{1}, 20_000), "huge")
	if c.Has(newTestHash(t, 5)) || c.Len() != 1 || c.Bytes() != offHeapPageSize {
		t.Fatalf("an over-budget entry was kept or left its old value behind")
	}

	// AddBuffer takes ownership of a buffer, and Remove releases its weight.
	c.AddBuffer(newTestHash(t, 9), memory.Malloc[byte](100), "buffer")
	if !c.Has(newTestHash(t, 9)) || c.Bytes() != 2*offHeapPageSize {
		t.Fatalf("AddBuffer: %d entries weigh %d bytes", c.Len(), c.Bytes())
	}
	c.Remove(newTestHash(t, 9))
	if c.Has(newTestHash(t, 9)) || c.Bytes() != offHeapPageSize {
		t.Fatalf("Remove did not release the entry's weight")
	}
}

// TestOffHeapFreesBuffersWhenDropped pins that an unreachable cache frees its buffers. They are
// invisible to the collector, so without the cleanup every dropped store - one per staging-consensus
// swap, one per test consensus - would leak its whole cache.
func TestOffHeapFreesBuffersWhenDropped(t *testing.T) {
	// Observe the allocator's synchronized accounting rather than reading cache state
	// concurrently with the runtime cleanup. No tests in this package run in parallel.
	t.Setenv("MEMORY_ALLOCATIONS", "1")
	waitForFree := func() {
		t.Helper()
		deadline := time.After(10 * time.Second)
		for {
			runtime.GC()
			if memory.LogLeaks() == 0 {
				return
			}
			select {
			case <-deadline:
				t.Fatal("the dropped cache did not free its buffers")
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	// Drain any caches from earlier tests before tracking this cache's allocation.
	waitForFree()
	func() {
		c := NewOffHeap[string](10, 1<<20, false)
		c.Add(newTestHash(t, 1), []byte{1, 2, 3}, "")
		if count := memory.LogLeaks(); count != 1 {
			t.Fatalf("the live cache has %d allocations, want 1", count)
		}
		runtime.KeepAlive(c)
	}()
	waitForFree()
}
