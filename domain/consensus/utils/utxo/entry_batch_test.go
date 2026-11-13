package utxo

import (
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/util/memory"
)

func TestEntryBatchSharesOnlyAMatchingScript(t *testing.T) {
	shared := &externalapi.ScriptPublicKey{Script: []byte{0x51, 0x21, 0x02}, Version: 0}
	batch := NewEntryBatch(shared, 0, nil)

	// Equal bytes in a different array, as a decoder hands them over.
	reused := []byte{0x51, 0x21, 0x02}
	first := batch.New(1, reused, 0, false, 10)
	second := batch.New(2, reused, 0, true, 20)
	if first.ScriptPublicKey() != second.ScriptPublicKey() {
		t.Fatal("two entries paying to the shared script got separate ScriptPublicKeys")
	}
	if first.ScriptPublicKey() == shared {
		t.Fatal("the batch shares the caller's ScriptPublicKey instead of its own copy")
	}

	other := []byte{0x52}
	differentScript := batch.New(3, other, 0, false, 30)
	differentVersion := batch.New(4, reused, 1, false, 40)
	for name, entry := range map[string]externalapi.UTXOEntry{"script": differentScript, "version": differentVersion} {
		if entry.ScriptPublicKey() == first.ScriptPublicKey() {
			t.Fatalf("an entry with a different %s got the shared ScriptPublicKey", name)
		}
	}

	// A decoder reuses its buffer, so New must not keep what it was handed.
	reused[0], other[0] = 0, 0
	shared.Script[0] = 0
	if first.ScriptPublicKey().Script[0] != 0x51 || differentScript.ScriptPublicKey().Script[0] != 0x52 ||
		differentVersion.ScriptPublicKey().Script[0] != 0x51 {
		t.Fatal("an entry's script aliases bytes the caller passed in")
	}

	if second.Amount() != 2 || !second.IsCoinbase() || second.BlockDAAScore() != 20 {
		t.Fatalf("entry holds amount=%d coinbase=%t daaScore=%d, want 2, true, 20",
			second.Amount(), second.IsCoinbase(), second.BlockDAAScore())
	}
}

func TestEntryBatchChunksFollowTheHintAndNeverMove(t *testing.T) {
	var chunks []any
	batch := NewEntryBatch(nil, 3, func(chunk any) { chunks = append(chunks, chunk) })

	const count = 3 + entryBatchChunkLen + 1
	entries := make([]externalapi.UTXOEntry, count)
	for i := range entries {
		entries[i] = batch.New(uint64(i), nil, 0, false, uint64(i))
	}
	// 3 from the hint, then full chunks once the hint is used up.
	if len(chunks) != 3 {
		t.Fatalf("got %d chunks for %d entries with a hint of 3, want 3", len(chunks), count)
	}
	for i, entry := range entries {
		if entry.Amount() != uint64(i) || entry.BlockDAAScore() != uint64(i) {
			t.Fatalf("entry %d reads amount %d, want %d: a later chunk overwrote it", i, entry.Amount(), i)
		}
	}
}

// The point of the batch: entries kept only in a memory.Block - which the collector does not scan -
// stay alive through one retained reference per chunk, and are let go when the block is freed.
func TestEntryBatchKeepsEntriesAliveThroughABlock(t *testing.T) {
	const count = 3 * entryBatchChunkLen
	var finalized atomic.Int64
	block := fillBlockFromBatch(count, &finalized)

	for range 3 {
		runtime.GC()
	}
	if got := finalized.Load(); got != 0 {
		t.Fatalf("%d chunks were collected while the block held their entries", got)
	}
	for i, entry := range block.Slice() {
		if entry.Amount() != uint64(i) {
			t.Fatalf("entry %d reads amount %d after GC, want %d", i, entry.Amount(), i)
		}
	}

	memory.Free(block)
	deadline := time.Now().Add(10 * time.Second)
	for finalized.Load() != 3 && time.Now().Before(deadline) {
		runtime.GC()
		time.Sleep(10 * time.Millisecond)
	}
	if got := finalized.Load(); got != 3 {
		t.Fatalf("%d of 3 chunks were collected after the block was freed", got)
	}
}

// fillBlockFromBatch is its own function so neither the batch nor an entry stays referenced from
// the test's stack frame.
//
//go:noinline
func fillBlockFromBatch(count int, finalized *atomic.Int64) *memory.Block[externalapi.UTXOEntry] {
	block := memory.Malloc[externalapi.UTXOEntry](count)
	batch := NewEntryBatch(nil, count, func(chunk any) {
		runtime.SetFinalizer(chunk, func(any) { finalized.Add(1) })
		block.Retain(chunk)
	})
	entries := block.Slice()
	for i := range entries {
		entries[i] = batch.New(uint64(i), []byte{byte(i)}, 0, false, 0)
	}
	return block
}
