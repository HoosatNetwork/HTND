package blockrelay

import (
	"errors"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/app/appmessage"
	"github.com/HoosatNetwork/HTND/app/protocol/protocolerrors"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/domain/dagconfig"
	"github.com/HoosatNetwork/HTND/infrastructure/config"
	"github.com/HoosatNetwork/HTND/infrastructure/network/netadapter/router"
)

type receiveBlocksIBDContext struct {
	IBDContext
	config *config.Config
}

func (c receiveBlocksIBDContext) Config() *config.Config { return c.config }

func newReceiveBlocksTestFlow(dequeueTimeout time.Duration) *handleIBDFlow {
	return &handleIBDFlow{
		IBDContext: receiveBlocksIBDContext{
			config: &config.Config{Flags: &config.Flags{IBDDequeueTimeout: dequeueTimeout}},
		},
		incomingRoute: router.NewRoute("incoming"),
		outgoingRoute: router.NewRoute("outgoing"),
	}
}

func enqueueIBDBlock(t *testing.T, route *router.Route, block *externalapi.DomainBlock) {
	if err := route.Enqueue(appmessage.NewMsgIBDBlock(appmessage.DomainBlockToMsgBlock(block))); err != nil {
		t.Fatalf("Enqueue: %+v", err)
	}
}

// TestReceiveRequestedIBDBlocksIgnoresUnrequestedBlocks pins that a block this batch did not ask for -
// for example a late duplicate answering an earlier batch's retry - does not count toward the batch.
// Counting it ended the receive loop before every requested block had arrived, and the processing
// loop then skipped the missing ones without inserting their bodies.
func TestReceiveRequestedIBDBlocksIgnoresUnrequestedBlocks(t *testing.T) {
	flow := newReceiveBlocksTestFlow(2 * time.Second)

	requested := []*externalapi.DomainBlock{dagconfig.MainnetParams.GenesisBlock, dagconfig.TestnetParams.GenesisBlock}
	stray := dagconfig.SimnetParams.GenesisBlock

	hashesToRequest := make([]*externalapi.DomainHash, len(requested))
	for i, block := range requested {
		hashesToRequest[i] = consensushashing.BlockHash(block)
	}
	strayHash := consensushashing.BlockHash(stray)
	for _, hash := range hashesToRequest {
		if hash.Equal(strayHash) {
			t.Fatalf("test blocks must have distinct hashes")
		}
	}

	enqueueIBDBlock(t, flow.incomingRoute, stray)
	for _, block := range requested {
		enqueueIBDBlock(t, flow.incomingRoute, block)
	}

	receivedBlocks := make(map[externalapi.DomainHash]*externalapi.DomainBlock)
	if _, err := flow.receiveRequestedIBDBlocks(hashesToRequest, receivedBlocks, time.Now()); err != nil {
		t.Fatalf("receiveRequestedIBDBlocks: %+v", err)
	}

	for _, hash := range hashesToRequest {
		if _, ok := receivedBlocks[*hash]; !ok {
			t.Fatalf("requested block %s was not received before the loop returned", hash)
		}
	}
	if _, ok := receivedBlocks[*strayHash]; ok {
		t.Fatalf("unrequested block %s should not be kept", strayHash)
	}
}

// TestReceiveRequestedIBDBlocksGivesUpOnSilentPeer pins that a peer which never answers is eventually
// abandoned with a protocol error, so the peer is disconnected and IBD can move on to another one,
// rather than being re-asked forever.
func TestReceiveRequestedIBDBlocksGivesUpOnSilentPeer(t *testing.T) {
	flow := newReceiveBlocksTestFlow(10 * time.Millisecond)
	hashesToRequest := []*externalapi.DomainHash{consensushashing.BlockHash(dagconfig.MainnetParams.GenesisBlock)}

	type result struct {
		retryCount int
		err        error
	}
	done := make(chan result, 1)
	go func() {
		retryCount, err := flow.receiveRequestedIBDBlocks(hashesToRequest,
			make(map[externalapi.DomainHash]*externalapi.DomainBlock), time.Now())
		done <- result{retryCount, err}
	}()

	select {
	case res := <-done:
		var protocolErr protocolerrors.ProtocolError
		if !errors.As(res.err, &protocolErr) {
			t.Fatalf("expected a protocol error, got %v", res.err)
		}
		if res.retryCount != maxIBDBlockRequestRetries {
			t.Fatalf("expected %d retries, got %d", maxIBDBlockRequestRetries, res.retryCount)
		}
		if got := flow.outgoingRoute.Length(); got != maxIBDBlockRequestRetries {
			t.Fatalf("expected %d re-requests to be sent, got %d", maxIBDBlockRequestRetries, got)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("receiveRequestedIBDBlocks kept waiting on a peer that never answers")
	}
}
