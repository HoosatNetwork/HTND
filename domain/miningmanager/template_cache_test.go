package miningmanager

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
)

// countingBuilder records how often a template is built from consensus versus rewritten for a
// different coinbase. A non-nil gate makes the first build wait, so concurrent polls can be
// shown to share that one build.
type countingBuilder struct {
	builds   atomic.Int32
	modifies atomic.Int32
	gate     chan struct{}
}

func (b *countingBuilder) BuildBlockTemplate(coinbaseData *externalapi.DomainCoinbaseData) (*externalapi.DomainBlockTemplate, error) {
	n := b.builds.Add(1)
	if b.gate != nil && n == 1 {
		<-b.gate
	}
	return &externalapi.DomainBlockTemplate{
		Block:          &externalapi.DomainBlock{Transactions: []*externalapi.DomainTransaction{}},
		CoinbaseData:   coinbaseData,
		IsNearlySynced: true,
	}, nil
}

func (b *countingBuilder) ModifyBlockTemplate(newCoinbaseData *externalapi.DomainCoinbaseData, blockTemplateToModify *externalapi.DomainBlockTemplate) (*externalapi.DomainBlockTemplate, error) {
	b.modifies.Add(1)
	blockTemplateToModify.CoinbaseData = newCoinbaseData
	blockTemplateToModify.Block = &externalapi.DomainBlock{PoWHash: "modified", Transactions: []*externalapi.DomainTransaction{}}
	return blockTemplateToModify, nil
}

func testCoinbase(extra string) *externalapi.DomainCoinbaseData {
	return &externalapi.DomainCoinbaseData{
		ScriptPublicKey: &externalapi.ScriptPublicKey{Script: []byte{0x51}, Version: 0},
		ExtraData:       []byte(extra),
	}
}

func newCachingManager(builder *countingBuilder) *miningManager {
	return &miningManager{
		blockTemplateBuilder: builder,
		cacheLock:            &sync.Mutex{},
	}
}

func TestGetBlockTemplateReusesCacheForIdenticalCoinbase(t *testing.T) {
	builder := &countingBuilder{}
	mm := newCachingManager(builder)
	coinbase := testCoinbase("miner")

	first, synced, err := mm.GetBlockTemplate(coinbase)
	if err != nil {
		t.Fatalf("first GetBlockTemplate: %v", err)
	}
	if !synced {
		t.Fatal("expected the built template to report nearly synced")
	}
	second, _, err := mm.GetBlockTemplate(coinbase)
	if err != nil {
		t.Fatalf("second GetBlockTemplate: %v", err)
	}
	if builder.builds.Load() != 1 {
		t.Fatalf("identical coinbase within the cache window built %d templates, want 1", builder.builds.Load())
	}
	if second != first {
		t.Fatal("cache hit returned a different block than the one just built")
	}
}

func TestGetBlockTemplateRebuildsAfterClearAndAfterMaxAge(t *testing.T) {
	builder := &countingBuilder{}
	mm := newCachingManager(builder)
	coinbase := testCoinbase("miner")

	if _, _, err := mm.GetBlockTemplate(coinbase); err != nil {
		t.Fatalf("first GetBlockTemplate: %v", err)
	}
	mm.ClearBlockTemplate()
	if _, _, err := mm.GetBlockTemplate(coinbase); err != nil {
		t.Fatalf("GetBlockTemplate after clear: %v", err)
	}
	if builder.builds.Load() != 2 {
		t.Fatalf("clearing the cache built %d templates, want 2", builder.builds.Load())
	}

	mm.cacheLock.Lock()
	mm.cachingTime = time.Now().Add(-blockTemplateCacheMaxAge - time.Millisecond)
	mm.cacheLock.Unlock()
	if _, _, err := mm.GetBlockTemplate(coinbase); err != nil {
		t.Fatalf("GetBlockTemplate after expiry: %v", err)
	}
	if builder.builds.Load() != 3 {
		t.Fatalf("expired cache built %d templates, want 3", builder.builds.Load())
	}
}

func TestGetBlockTemplateModifiesCachedTemplateForNewCoinbase(t *testing.T) {
	builder := &countingBuilder{}
	mm := newCachingManager(builder)

	if _, _, err := mm.GetBlockTemplate(testCoinbase("miner-a")); err != nil {
		t.Fatalf("first GetBlockTemplate: %v", err)
	}
	block, _, err := mm.GetBlockTemplate(testCoinbase("miner-b"))
	if err != nil {
		t.Fatalf("GetBlockTemplate with a different coinbase: %v", err)
	}
	if builder.builds.Load() != 1 || builder.modifies.Load() != 1 {
		t.Fatalf("builds=%d modifies=%d, want one build and one coinbase rewrite", builder.builds.Load(), builder.modifies.Load())
	}
	if block.PoWHash != "modified" {
		t.Fatalf("different coinbase returned PoW hash %q, want the rewritten template", block.PoWHash)
	}
}

func TestGetBlockTemplateConcurrentPollsShareOneBuild(t *testing.T) {
	gate := make(chan struct{})
	builder := &countingBuilder{gate: gate}
	mm := newCachingManager(builder)
	coinbase := testCoinbase("miner")

	const polls = 8
	var wg sync.WaitGroup
	wg.Add(polls)
	errCh := make(chan error, polls)
	for range polls {
		go func() {
			defer wg.Done()
			if _, _, err := mm.GetBlockTemplate(coinbase); err != nil {
				errCh <- err
			}
		}()
	}

	// Let the polls pile up on the cache lock behind the one in-flight build, then release it.
	time.Sleep(50 * time.Millisecond)
	close(gate)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("GetBlockTemplate: %v", err)
	}
	if builder.builds.Load() != 1 {
		t.Fatalf("concurrent polls built %d templates, want 1", builder.builds.Load())
	}
}
