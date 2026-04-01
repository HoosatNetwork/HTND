package utxoindex

import (
	"os"
	"testing"

	"github.com/Hoosat-Oy/HTND/domain/consensus/database/binaryserialization"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	consensusutxo "github.com/Hoosat-Oy/HTND/domain/consensus/utils/utxo"
	"github.com/Hoosat-Oy/HTND/infrastructure/db/database/ldb"
	"github.com/Hoosat-Oy/HTND/util/memory"
)

func TestHasUTXOsUsesTrackedCounts(t *testing.T) {
	path, err := os.MkdirTemp("", "utxoindex-store")
	if err != nil {
		t.Fatalf("MkdirTemp unexpectedly failed: %s", err)
	}
	defer os.RemoveAll(path)

	db, err := ldb.NewLevelDB(path, 8)
	if err != nil {
		t.Fatalf("NewLevelDB unexpectedly failed: %s", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Fatalf("Close unexpectedly failed: %s", err)
		}
	}()

	store := newUTXOIndexStore(db)
	scriptPublicKey := &externalapi.ScriptPublicKey{Script: []byte{0x51, 0x21, 0x02}, Version: 0}
	if err := db.Put(circulatingSupplyKey, binaryserialization.SerializeUint64(0)); err != nil {
		t.Fatalf("initializing circulating supply unexpectedly failed: %s", err)
	}

	hasUTXOs, err := store.HasUTXOs(scriptPublicKey)
	if err != nil {
		t.Fatalf("HasUTXOs unexpectedly failed before writes: %s", err)
	}
	if hasUTXOs {
		t.Fatal("HasUTXOs unexpectedly returned true for an empty script")
	}

	entry := consensusutxo.NewUTXOEntry(1000, scriptPublicKey, false, 100)
	outpoint1 := &externalapi.DomainOutpoint{TransactionID: *externalapi.NewDomainTransactionIDFromByteArray(&[32]byte{1}), Index: 0}
	outpoint2 := &externalapi.DomainOutpoint{TransactionID: *externalapi.NewDomainTransactionIDFromByteArray(&[32]byte{2}), Index: 1}

	if err := store.add(scriptPublicKey, outpoint1, entry); err != nil {
		t.Fatalf("add unexpectedly failed: %s", err)
	}
	if err := store.add(scriptPublicKey, outpoint2, entry); err != nil {
		t.Fatalf("second add unexpectedly failed: %s", err)
	}
	if err := store.commit(); err != nil {
		t.Fatalf("commit unexpectedly failed: %s", err)
	}

	hasUTXOs, err = store.HasUTXOs(scriptPublicKey)
	if err != nil {
		t.Fatalf("HasUTXOs unexpectedly failed after commit: %s", err)
	}
	if !hasUTXOs {
		t.Fatal("HasUTXOs unexpectedly returned false after adding UTXOs")
	}

	if err := store.remove(scriptPublicKey, outpoint1, entry); err != nil {
		t.Fatalf("remove unexpectedly failed: %s", err)
	}
	if err := store.commit(); err != nil {
		t.Fatalf("commit after first remove unexpectedly failed: %s", err)
	}

	hasUTXOs, err = store.HasUTXOs(scriptPublicKey)
	if err != nil {
		t.Fatalf("HasUTXOs unexpectedly failed after partial remove: %s", err)
	}
	if !hasUTXOs {
		t.Fatal("HasUTXOs unexpectedly returned false while one UTXO remains")
	}

	if err := store.remove(scriptPublicKey, outpoint2, entry); err != nil {
		t.Fatalf("second remove unexpectedly failed: %s", err)
	}
	if err := store.commit(); err != nil {
		t.Fatalf("commit after second remove unexpectedly failed: %s", err)
	}

	hasUTXOs, err = store.HasUTXOs(scriptPublicKey)
	if err != nil {
		t.Fatalf("HasUTXOs unexpectedly failed after removing all UTXOs: %s", err)
	}
	if hasUTXOs {
		t.Fatal("HasUTXOs unexpectedly returned true after removing all UTXOs")
	}
}

func TestUTXOsReturnsReallocatedBufferForCallerCleanup(t *testing.T) {
	if leaks := memory.LogLeaks(); leaks != 0 {
		t.Fatalf("expected no outstanding allocations before test, got %d", leaks)
	}

	path, err := os.MkdirTemp("", "utxoindex-store")
	if err != nil {
		t.Fatalf("MkdirTemp unexpectedly failed: %s", err)
	}
	defer os.RemoveAll(path)

	db, err := ldb.NewLevelDB(path, 8)
	if err != nil {
		t.Fatalf("NewLevelDB unexpectedly failed: %s", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Fatalf("Close unexpectedly failed: %s", err)
		}
	}()

	store := newUTXOIndexStore(db)
	scriptPublicKey := &externalapi.ScriptPublicKey{Script: []byte{0x51, 0x21, 0x02}, Version: 0}
	entry := consensusutxo.NewUTXOEntry(1000, scriptPublicKey, false, 100)
	if err := db.Put(circulatingSupplyKey, binaryserialization.SerializeUint64(0)); err != nil {
		t.Fatalf("initializing circulating supply unexpectedly failed: %s", err)
	}

	for i := 0; i < 2; i++ {
		outpoint := &externalapi.DomainOutpoint{
			TransactionID: *externalapi.NewDomainTransactionIDFromByteArray(&[32]byte{byte(i + 1)}),
			Index:         uint32(i),
		}
		if err := store.add(scriptPublicKey, outpoint, entry); err != nil {
			t.Fatalf("add unexpectedly failed: %s", err)
		}
	}
	if err := store.commit(); err != nil {
		t.Fatalf("commit unexpectedly failed: %s", err)
	}

	buffer := memory.Malloc[UTXOPair](1)
	if buffer == nil {
		t.Fatal("expected initial buffer allocation")
	}

	pairs, updatedBuffer, err := store.UTXOs(scriptPublicKey, 0, buffer)
	if err != nil {
		memory.Free(buffer)
		t.Fatalf("UTXOs unexpectedly failed: %s", err)
	}
	if len(pairs) != 2 {
		memory.Free(updatedBuffer)
		t.Fatalf("expected 2 UTXO pairs, got %d", len(pairs))
	}
	if updatedBuffer == buffer {
		memory.Free(updatedBuffer)
		t.Fatal("expected UTXOs to return a reallocated buffer when capacity was insufficient")
	}

	memory.Free(updatedBuffer)

	if leaks := memory.LogLeaks(); leaks != 0 {
		t.Fatalf("expected no outstanding allocations after freeing returned buffer, got %d", leaks)
	}
}

// import (
// 	"sync"
// 	"testing"

// 	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
// 	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/utxo"
// )

// // Test thread-safety of scriptLRUCache
// func TestScriptLRUCacheConcurrency(t *testing.T) {
// 	cache := newScriptLRUCache(100)
// 	var wg sync.WaitGroup

// 	testPairs := []UTXOPair{
// 		{
// 			Outpoint: externalapi.DomainOutpoint{
// 				TransactionID: *externalapi.NewDomainTransactionIDFromByteArray(&[32]byte{1}),
// 				Index:         0,
// 			},
// 			Entry: utxo.NewUTXOEntry(
// 				1000,
// 				&externalapi.ScriptPublicKey{Script: []byte{1, 2, 3}, Version: 0},
// 				false,
// 				100,
// 			),
// 		},
// 	}

// 	// Concurrent writes
// 	for i := range 100 {
// 		wg.Add(1)
// 		go func(idx int) {
// 			defer wg.Done()
// 			key := string([]byte{byte(idx)})
// 			cache.Put(key, testPairs)
// 		}(i)
// 	}

// 	// Concurrent reads
// 	for i := range 100 {
// 		wg.Add(1)
// 		go func(idx int) {
// 			defer wg.Done()
// 			key := string([]byte{byte(idx)})
// 			_, _ = cache.Get(key)
// 		}(i)
// 	}

// 	// Concurrent deletes
// 	for i := range 50 {
// 		wg.Add(1)
// 		go func(idx int) {
// 			defer wg.Done()
// 			key := string([]byte{byte(idx)})
// 			cache.Delete(key)
// 		}(i)
// 	}

// 	wg.Wait()
// 	// If we get here without data races or crashes, test passes
// }

// // Test that scriptLRUCache returns copies
// func TestScriptLRUCacheImmutability(t *testing.T) {
// 	cache := newScriptLRUCache(10)

// 	originalPairs := []UTXOPair{
// 		{
// 			Outpoint: externalapi.DomainOutpoint{
// 				TransactionID: *externalapi.NewDomainTransactionIDFromByteArray(&[32]byte{1}),
// 				Index:         0,
// 			},
// 			Entry: utxo.NewUTXOEntry(
// 				1000,
// 				&externalapi.ScriptPublicKey{Script: []byte{1, 2, 3}, Version: 0},
// 				false,
// 				100,
// 			),
// 		},
// 	}

// 	key := "test-key"
// 	cache.Put(key, originalPairs)

// 	// Get the cached value
// 	retrieved1, ok := cache.Get(key)
// 	if !ok {
// 		t.Fatal("Expected to find cached value")
// 	}

// 	// Modify the retrieved slice
// 	retrieved1[0].Outpoint.Index = 999

// 	// Get again and verify it wasn't affected
// 	retrieved2, ok := cache.Get(key)
// 	if !ok {
// 		t.Fatal("Expected to find cached value")
// 	}

// 	if retrieved2[0].Outpoint.Index != 0 {
// 		t.Errorf("Cache value was mutated. Expected index 0, got %d", retrieved2[0].Outpoint.Index)
// 	}
// }

// // Test cache eviction works correctly
// func TestScriptLRUCacheEviction(t *testing.T) {
// 	cache := newScriptLRUCache(3) // Small cache for testing eviction

// 	pairs := []UTXOPair{
// 		{
// 			Outpoint: externalapi.DomainOutpoint{
// 				TransactionID: *externalapi.NewDomainTransactionIDFromByteArray(&[32]byte{1}),
// 				Index:         0,
// 			},
// 			Entry: utxo.NewUTXOEntry(
// 				1000,
// 				&externalapi.ScriptPublicKey{Script: []byte{1, 2, 3}, Version: 0},
// 				false,
// 				100,
// 			),
// 		},
// 	}

// 	// Fill cache (key1 is oldest, key3 is newest)
// 	cache.Put("key1", pairs)
// 	cache.Put("key2", pairs)
// 	cache.Put("key3", pairs)

// 	// Add one more, should evict the oldest (key1)
// 	cache.Put("key4", pairs)

// 	// key1 should be evicted (oldest, least recently used)
// 	if _, ok := cache.Get("key1"); ok {
// 		t.Error("key1 should have been evicted")
// 	}

// 	// Others should still be present
// 	if _, ok := cache.Get("key2"); !ok {
// 		t.Error("key2 should still be in cache")
// 	}
// 	if _, ok := cache.Get("key3"); !ok {
// 		t.Error("key3 should still be in cache")
// 	}
// 	if _, ok := cache.Get("key4"); !ok {
// 		t.Error("key4 should be in cache")
// 	}
// }

// // Test copyUTXOPairs
// func TestCopyUTXOPairs(t *testing.T) {
// 	original := []UTXOPair{
// 		{
// 			Outpoint: externalapi.DomainOutpoint{
// 				TransactionID: *externalapi.NewDomainTransactionIDFromByteArray(&[32]byte{1}),
// 				Index:         0,
// 			},
// 			Entry: utxo.NewUTXOEntry(
// 				1000,
// 				&externalapi.ScriptPublicKey{Script: []byte{1, 2, 3}, Version: 0},
// 				false,
// 				100,
// 			),
// 		},
// 	}

// 	copied := copyUTXOPairs(original)

// 	// Verify it's a different slice (not the same underlying array)
// 	// Note: The UTXOPair elements themselves are copied by value,
// 	// but nested pointers (like UTXOEntry interface) are shared.
// 	// This is intentional and safe since UTXOEntry is immutable.
// 	if &original[0] == &copied[0] {
// 		t.Error("Copy should create a new slice, not reference the same one")
// 	}

// 	// Modify original slice's outpoint
// 	original[0].Outpoint.Index = 999

// 	// Verify copy is unchanged (slice was copied, so this element is independent)
// 	if copied[0].Outpoint.Index != 0 {
// 		t.Error("Copy was affected by changes to original")
// 	}

// 	// Test nil case
// 	if copyUTXOPairs(nil) != nil {
// 		t.Error("Copying nil should return nil")
// 	}
// }

// // Benchmark per-script cache hit performance
// func BenchmarkScriptCacheHit(b *testing.B) {
// 	cache := newScriptLRUCache(10000)

// 	// Populate cache with test data
// 	pairs := make([]UTXOPair, 100)
// 	for i := range 100 {
// 		pairs[i] = UTXOPair{
// 			Outpoint: externalapi.DomainOutpoint{
// 				TransactionID: *externalapi.NewDomainTransactionIDFromByteArray(&[32]byte{byte(i)}),
// 				Index:         uint32(i),
// 			},
// 			Entry: utxo.NewUTXOEntry(
// 				1000,
// 				&externalapi.ScriptPublicKey{Script: []byte{1, 2, 3}, Version: 0},
// 				false,
// 				100,
// 			),
// 		}
// 	}
// 	cache.Put("test-key", pairs)

// 	b.ResetTimer()
// 	for i := 0; i < b.N; i++ {
// 		_, _ = cache.Get("test-key")
// 	}
// }

// // Benchmark per-script cache miss performance
// func BenchmarkScriptCacheMiss(b *testing.B) {
// 	cache := newScriptLRUCache(10000)

// 	b.ResetTimer()
// 	for i := 0; i < b.N; i++ {
// 		_, _ = cache.Get("nonexistent-key")
// 	}
// }

// // Benchmark per-script cache put performance
// func BenchmarkScriptCachePut(b *testing.B) {
// 	cache := newScriptLRUCache(10000)

// 	// Create test data
// 	pairs := make([]UTXOPair, 100)
// 	for i := range 100 {
// 		pairs[i] = UTXOPair{
// 			Outpoint: externalapi.DomainOutpoint{
// 				TransactionID: *externalapi.NewDomainTransactionIDFromByteArray(&[32]byte{byte(i)}),
// 				Index:         uint32(i),
// 			},
// 			Entry: utxo.NewUTXOEntry(
// 				1000,
// 				&externalapi.ScriptPublicKey{Script: []byte{1, 2, 3}, Version: 0},
// 				false,
// 				100,
// 			),
// 		}
// 	}

// 	b.ResetTimer()
// 	for i := 0; i < b.N; i++ {
// 		key := string([]byte{byte(i % 256)})
// 		cache.Put(key, pairs)
// 	}
// }
