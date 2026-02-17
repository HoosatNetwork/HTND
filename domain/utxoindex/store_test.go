package utxoindex

import (
	"sync"
	"testing"

	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/utxo"
)

// Test thread-safety of utxoLRUCache
func TestUTXOLRUCacheConcurrency(t *testing.T) {
	cache := newUtxoLRUCache(100)
	var wg sync.WaitGroup

	// Create some test data
	testOutpoint := externalapi.DomainOutpoint{
		TransactionID: *externalapi.NewDomainTransactionIDFromByteArray(&[32]byte{1}),
		Index:         0,
	}
	testEntry := utxo.NewUTXOEntry(
		1000,
		&externalapi.ScriptPublicKey{Script: []byte{1, 2, 3}, Version: 0},
		false,
		100,
	)

	// Concurrent writes
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			outpoint := externalapi.DomainOutpoint{
				TransactionID: *externalapi.NewDomainTransactionIDFromByteArray(&[32]byte{byte(idx)}),
				Index:         uint32(idx),
			}
			cache.Put(outpoint, testEntry)
		}(i)
	}

	// Concurrent reads
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = cache.Get(testOutpoint)
		}()
	}

	// Concurrent deletes
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			outpoint := externalapi.DomainOutpoint{
				TransactionID: *externalapi.NewDomainTransactionIDFromByteArray(&[32]byte{byte(idx)}),
				Index:         uint32(idx),
			}
			cache.Delete(outpoint)
		}(i)
	}

	wg.Wait()
	// If we get here without data races or crashes, test passes
}

// Test thread-safety of scriptLRUCache
func TestScriptLRUCacheConcurrency(t *testing.T) {
	cache := newScriptLRUCache(100)
	var wg sync.WaitGroup

	testPairs := []UTXOPair{
		{
			Outpoint: externalapi.DomainOutpoint{
				TransactionID: *externalapi.NewDomainTransactionIDFromByteArray(&[32]byte{1}),
				Index:         0,
			},
			Entry: utxo.NewUTXOEntry(
				1000,
				&externalapi.ScriptPublicKey{Script: []byte{1, 2, 3}, Version: 0},
				false,
				100,
			),
		},
	}

	// Concurrent writes
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			key := string([]byte{byte(idx)})
			cache.Put(key, testPairs)
		}(i)
	}

	// Concurrent reads
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			key := string([]byte{byte(idx)})
			_, _ = cache.Get(key)
		}(i)
	}

	// Concurrent deletes
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			key := string([]byte{byte(idx)})
			cache.Delete(key)
		}(i)
	}

	wg.Wait()
	// If we get here without data races or crashes, test passes
}

// Test that scriptLRUCache returns copies
func TestScriptLRUCacheImmutability(t *testing.T) {
	cache := newScriptLRUCache(10)

	originalPairs := []UTXOPair{
		{
			Outpoint: externalapi.DomainOutpoint{
				TransactionID: *externalapi.NewDomainTransactionIDFromByteArray(&[32]byte{1}),
				Index:         0,
			},
			Entry: utxo.NewUTXOEntry(
				1000,
				&externalapi.ScriptPublicKey{Script: []byte{1, 2, 3}, Version: 0},
				false,
				100,
			),
		},
	}

	key := "test-key"
	cache.Put(key, originalPairs)

	// Get the cached value
	retrieved1, ok := cache.Get(key)
	if !ok {
		t.Fatal("Expected to find cached value")
	}

	// Modify the retrieved slice
	retrieved1[0].Outpoint.Index = 999

	// Get again and verify it wasn't affected
	retrieved2, ok := cache.Get(key)
	if !ok {
		t.Fatal("Expected to find cached value")
	}

	if retrieved2[0].Outpoint.Index != 0 {
		t.Errorf("Cache value was mutated. Expected index 0, got %d", retrieved2[0].Outpoint.Index)
	}
}

// Test cache eviction works correctly
func TestScriptLRUCacheEviction(t *testing.T) {
	cache := newScriptLRUCache(3) // Small cache for testing eviction

	pairs := []UTXOPair{
		{
			Outpoint: externalapi.DomainOutpoint{
				TransactionID: *externalapi.NewDomainTransactionIDFromByteArray(&[32]byte{1}),
				Index:         0,
			},
			Entry: utxo.NewUTXOEntry(
				1000,
				&externalapi.ScriptPublicKey{Script: []byte{1, 2, 3}, Version: 0},
				false,
				100,
			),
		},
	}

	// Fill cache (key1 is oldest, key3 is newest)
	cache.Put("key1", pairs)
	cache.Put("key2", pairs)
	cache.Put("key3", pairs)

	// Add one more, should evict the oldest (key1)
	cache.Put("key4", pairs)

	// key1 should be evicted (oldest, least recently used)
	if _, ok := cache.Get("key1"); ok {
		t.Error("key1 should have been evicted")
	}

	// Others should still be present
	if _, ok := cache.Get("key2"); !ok {
		t.Error("key2 should still be in cache")
	}
	if _, ok := cache.Get("key3"); !ok {
		t.Error("key3 should still be in cache")
	}
	if _, ok := cache.Get("key4"); !ok {
		t.Error("key4 should be in cache")
	}
}

// Test copyUTXOPairs
func TestCopyUTXOPairs(t *testing.T) {
	original := []UTXOPair{
		{
			Outpoint: externalapi.DomainOutpoint{
				TransactionID: *externalapi.NewDomainTransactionIDFromByteArray(&[32]byte{1}),
				Index:         0,
			},
			Entry: utxo.NewUTXOEntry(
				1000,
				&externalapi.ScriptPublicKey{Script: []byte{1, 2, 3}, Version: 0},
				false,
				100,
			),
		},
	}

	copied := copyUTXOPairs(original)

	// Verify it's a different slice (not the same underlying array)
	// Note: The UTXOPair elements themselves are copied by value,
	// but nested pointers (like UTXOEntry interface) are shared.
	// This is intentional and safe since UTXOEntry is immutable.
	if &original[0] == &copied[0] {
		t.Error("Copy should create a new slice, not reference the same one")
	}

	// Modify original slice's outpoint
	original[0].Outpoint.Index = 999

	// Verify copy is unchanged (slice was copied, so this element is independent)
	if copied[0].Outpoint.Index != 0 {
		t.Error("Copy was affected by changes to original")
	}

	// Test nil case
	if copyUTXOPairs(nil) != nil {
		t.Error("Copying nil should return nil")
	}
}

// Test Clear methods
func TestCacheClear(t *testing.T) {
	// Test utxoLRUCache.Clear()
	utxoCache := newUtxoLRUCache(10)
	testOutpoint := externalapi.DomainOutpoint{
		TransactionID: *externalapi.NewDomainTransactionIDFromByteArray(&[32]byte{1}),
		Index:         0,
	}
	testEntry := utxo.NewUTXOEntry(
		1000,
		&externalapi.ScriptPublicKey{Script: []byte{1, 2, 3}, Version: 0},
		false,
		100,
	)
	utxoCache.Put(testOutpoint, testEntry)
	utxoCache.Clear()
	if _, ok := utxoCache.Get(testOutpoint); ok {
		t.Error("utxoCache should be empty after Clear()")
	}

	// Test scriptLRUCache.Clear()
	scriptCache := newScriptLRUCache(10)
	pairs := []UTXOPair{{Outpoint: testOutpoint, Entry: testEntry}}
	scriptCache.Put("key", pairs)
	scriptCache.Clear()
	if _, ok := scriptCache.Get("key"); ok {
		t.Error("scriptCache should be empty after Clear()")
	}
}

// Benchmark per-script cache hit performance
func BenchmarkScriptCacheHit(b *testing.B) {
	cache := newScriptLRUCache(10000)

	// Populate cache with test data
	pairs := make([]UTXOPair, 100)
	for i := 0; i < 100; i++ {
		pairs[i] = UTXOPair{
			Outpoint: externalapi.DomainOutpoint{
				TransactionID: *externalapi.NewDomainTransactionIDFromByteArray(&[32]byte{byte(i)}),
				Index:         uint32(i),
			},
			Entry: utxo.NewUTXOEntry(
				1000,
				&externalapi.ScriptPublicKey{Script: []byte{1, 2, 3}, Version: 0},
				false,
				100,
			),
		}
	}
	cache.Put("test-key", pairs)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = cache.Get("test-key")
	}
}

// Benchmark per-script cache miss performance
func BenchmarkScriptCacheMiss(b *testing.B) {
	cache := newScriptLRUCache(10000)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = cache.Get("nonexistent-key")
	}
}

// Benchmark per-script cache put performance
func BenchmarkScriptCachePut(b *testing.B) {
	cache := newScriptLRUCache(10000)

	// Create test data
	pairs := make([]UTXOPair, 100)
	for i := 0; i < 100; i++ {
		pairs[i] = UTXOPair{
			Outpoint: externalapi.DomainOutpoint{
				TransactionID: *externalapi.NewDomainTransactionIDFromByteArray(&[32]byte{byte(i)}),
				Index:         uint32(i),
			},
			Entry: utxo.NewUTXOEntry(
				1000,
				&externalapi.ScriptPublicKey{Script: []byte{1, 2, 3}, Version: 0},
				false,
				100,
			),
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		key := string([]byte{byte(i % 256)})
		cache.Put(key, pairs)
	}
}
