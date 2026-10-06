package memory

import (
	"runtime"
	"sync/atomic"
	"testing"
)

type payload struct {
	value int
	// A pointer field, so the struct itself is not pointer-free either.
	label *string
}

// TestBlockKeepsPointersAliveForTheCollector is the regression test for a node crash: a nil
// pointer dereference deep inside DbUtxoDiff.MarshalVT, on a line guarded by an explicit nil check
// one frame up.
//
// The cause was that Malloc backed every block with mmap'ed memory, which the garbage collector
// neither treats as a root nor scans. A block of []*DbUtxoCollectionItem therefore held the only
// references to those items in memory the collector could not see, so it was free to reclaim them
// while the block was still using them.
//
// This test stores pointers in a block, drops every other reference, and forces collection. If the
// block's memory is invisible to the collector the objects become unreachable and their finalizers
// run - which is exactly the bug. Finalizers are used rather than reading the values back because
// reclaimed memory is not necessarily overwritten, so reading it can succeed by luck.
func TestBlockKeepsPointersAliveForTheCollector(t *testing.T) {
	const count = 64

	var finalized int32
	block := Malloc[*payload](count)
	if block == nil {
		t.Fatal("Malloc returned nil")
	}
	defer Free(block)

	slice := block.Slice()
	if len(slice) != count {
		t.Fatalf("expected a slice of %d, got %d", count, len(slice))
	}

	func() {
		for i := 0; i < count; i++ {
			label := "payload"
			item := &payload{value: i + 1, label: &label}
			runtime.SetFinalizer(item, func(*payload) {
				atomic.AddInt32(&finalized, 1)
			})
			slice[i] = item
		}
	}()

	// Two cycles: the first makes unreachable objects eligible, the second lets finalizers run.
	for i := 0; i < 3; i++ {
		runtime.GC()
	}

	if collected := atomic.LoadInt32(&finalized); collected != 0 {
		t.Fatalf("the collector reclaimed %d of %d objects the block still holds - the block's "+
			"memory is invisible to it", collected, count)
	}

	// And the values must still be intact and readable through the block.
	readBack := block.Slice()
	for i := 0; i < count; i++ {
		if readBack[i] == nil {
			t.Fatalf("entry %d read back as nil", i)
		}
		if readBack[i].value != i+1 {
			t.Fatalf("entry %d read back as %d, want %d", i, readBack[i].value, i+1)
		}
		if readBack[i].label == nil || *readBack[i].label != "payload" {
			t.Fatalf("entry %d lost its pointer field", i)
		}
	}
	runtime.KeepAlive(block)
}

// TestPointerFreeTypesStayOnMmap pins that the safety fix did not quietly move every allocation
// onto the Go heap. Pointer-free element types have no reachability problem, so they keep the
// mmap'ed backing.
func TestPointerFreeTypesStayOnMmap(t *testing.T) {
	type plain struct {
		a int64
		b uint32
	}
	block := Malloc[plain](8)
	if block == nil {
		t.Fatal("Malloc returned nil")
	}
	defer Free(block)
	if block.managed != nil {
		t.Error("a pointer-free element type should not need Go-managed backing")
	}
	if block.mem == nil {
		t.Error("a pointer-free element type should keep its mmap'ed backing")
	}
}

func TestContainsPointersClassification(t *testing.T) {
	type withString struct{ s string }
	type withArrayOfPointers struct{ a [4]*int }
	type nestedPlain struct {
		inner struct{ x, y int64 }
	}

	if containsPointers[int64]() {
		t.Error("int64 does not contain pointers")
	}
	if containsPointers[nestedPlain]() {
		t.Error("a nested plain struct does not contain pointers")
	}
	// A string is a pointer and a length. Missing this would have put string data back into
	// unscanned memory.
	if !containsPointers[withString]() {
		t.Error("a struct holding a string contains a pointer")
	}
	if !containsPointers[withArrayOfPointers]() {
		t.Error("an array of pointers contains pointers")
	}
	if !containsPointers[any]() {
		t.Error("an interface contains pointers")
	}
	if !containsPointers[*int]() {
		t.Error("a pointer contains a pointer")
	}
}
