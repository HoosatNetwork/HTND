package blockrelay

import (
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
)

// TestFetchIBDBlockBatchesRequestsNextBatchBeforeProcessing pins that the next body batch is already
// requested from the peer when the current one is processed, so the peer serves it while this node
// validates instead of both sides taking turns - and that it is not requested before the current
// batch has fully arrived, which would mix two batches' blocks in one receive.
func TestFetchIBDBlockBatchesRequestsNextBatchBeforeProcessing(t *testing.T) {
	flow := newReceiveBlocksTestFlow(2 * time.Second)

	blocks := []*externalapi.DomainBlock{
		dagconfig.MainnetParams.GenesisBlock,
		dagconfig.TestnetParams.GenesisBlock,
		dagconfig.SimnetParams.GenesisBlock,
	}
	hashes := make([]*externalapi.DomainHash, len(blocks))
	for i, block := range blocks {
		hashes[i] = consensushashing.BlockHash(block)
	}

	dequeueRequest := func() []*externalapi.DomainHash {
		t.Helper()
		message, err := flow.outgoingRoute.DequeueWithTimeout(time.Second)
		if err != nil {
			t.Fatalf("expected a block request: %+v", err)
		}
		return message.(*appmessage.MsgRequestIBDBlocks).Hashes
	}
	assertNoRequest := func(when string) {
		t.Helper()
		if _, err := flow.outgoingRoute.DequeueWithTimeout(10 * time.Millisecond); err == nil {
			t.Fatalf("unexpected block request %s", when)
		}
	}

	// The peer's answer to the first request. The route is buffered, so it can be queued up front.
	enqueueIBDBlock(t, flow.incomingRoute, blocks[0])

	var processed []*externalapi.DomainHash
	err := flow.fetchIBDBlockBatches(hashes, 1, func(batch []*externalapi.DomainHash,
		receivedBlocks map[externalapi.DomainHash]*externalapi.DomainBlock,
	) error {
		i := len(processed)
		if len(batch) != 1 || !batch[0].Equal(hashes[i]) {
			t.Fatalf("batch %d is %v, want [%s]", i, batch, hashes[i])
		}
		if _, ok := receivedBlocks[*hashes[i]]; !ok {
			t.Fatalf("batch %d was processed before its block arrived", i)
		}
		if i == 0 {
			if got := dequeueRequest(); len(got) != 1 || !got[0].Equal(hashes[0]) {
				t.Fatalf("first request is %v, want [%s]", got, hashes[0])
			}
		}
		if i+1 < len(hashes) {
			if got := dequeueRequest(); len(got) != 1 || !got[0].Equal(hashes[i+1]) {
				t.Fatalf("while processing batch %d the outstanding request is %v, want [%s]", i, got, hashes[i+1])
			}
			assertNoRequest("beyond the next batch")
			enqueueIBDBlock(t, flow.incomingRoute, blocks[i+1])
		} else {
			assertNoRequest("after the last batch")
		}
		processed = append(processed, batch[0])
		return nil
	})
	if err != nil {
		t.Fatalf("fetchIBDBlockBatches: %+v", err)
	}
	if len(processed) != len(hashes) {
		t.Fatalf("processed %d batches, want %d", len(processed), len(hashes))
	}
}
