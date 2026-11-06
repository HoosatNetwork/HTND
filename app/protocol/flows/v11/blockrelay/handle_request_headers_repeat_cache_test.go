package blockrelay

import (
	"math/big"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/HoosatNetwork/HTND/v2/domain"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/blockheader"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/router"
)

type repeatedHeadersRequestConsensus struct {
	externalapi.Consensus
	getHashesBetweenCalls int
}

func (c *repeatedHeadersRequestConsensus) GetBlockInfo(*externalapi.DomainHash) (*externalapi.BlockInfo, error) {
	return &externalapi.BlockInfo{Exists: true, BlockStatus: externalapi.StatusUTXOValid}, nil
}

func (c *repeatedHeadersRequestConsensus) IsInSelectedParentChainOf(*externalapi.DomainHash, *externalapi.DomainHash) (bool, error) {
	return true, nil
}

func (c *repeatedHeadersRequestConsensus) GetHashesBetween(lowHash, highHash *externalapi.DomainHash, _ uint64, _ bool) (
	[]*externalapi.DomainHash, *externalapi.DomainHash, error,
) {
	c.getHashesBetweenCalls++
	return []*externalapi.DomainHash{lowHash}, highHash, nil
}

func (c *repeatedHeadersRequestConsensus) GetBlockHeaders(blockHashes []*externalapi.DomainHash) ([]externalapi.BlockHeader, error) {
	headers := make([]externalapi.BlockHeader, len(blockHashes))
	for i := range blockHashes {
		headers[i] = blockheader.NewImmutableBlockHeader(1, nil, &externalapi.DomainHash{}, &externalapi.DomainHash{},
			&externalapi.DomainHash{}, 0, 0, 0, 0, 0, big.NewInt(0), &externalapi.DomainHash{})
	}
	return headers, nil
}

type repeatedHeadersRequestDomain struct {
	domain.Domain
	consensus externalapi.Consensus
}

func (d repeatedHeadersRequestDomain) Consensus() externalapi.Consensus { return d.consensus }

type repeatedHeadersRequestContext struct {
	domain domain.Domain
}

func (c repeatedHeadersRequestContext) Domain() domain.Domain { return c.domain }

// TestHandleRequestHeadersCachesAnIdenticalRepeatedRequest is part of the fix for mining stalls caused
// by a peer stuck resending an identical RequestHeaders(lowHash, highHash) instead of ever advancing.
// Each retry used to pay for GetHashesBetween and GetBlockHeaders again - both take the same consensus
// lock GetBlockTemplate and SubmitBlock need - so a peer doing this every few seconds for hours
// repeatedly stole lock time from mining for no reason.
//
// This pins the cache-hit path directly: the flow's cache fields are pre-seeded as if a real chunk was
// just served (constructing the flow directly, in-package, rather than going through the exported
// HandleRequestHeaders/logging path, which needs a fully wired *peer.Peer this test has no cheap way
// to construct), then an incoming request identical to that seed must be answered without a second
// GetHashesBetween call.
func TestHandleRequestHeadersCachesAnIdenticalRepeatedRequest(t *testing.T) {
	lowHash := externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{1})
	highHash := externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{2})

	consensus := &repeatedHeadersRequestConsensus{}
	context := repeatedHeadersRequestContext{domain: repeatedHeadersRequestDomain{consensus: consensus}}

	incoming := router.NewRoute("incoming")
	outgoing := router.NewRouteWithCapacity("outgoing", 10)

	flow := &handleRequestHeadersFlow{
		RequestHeadersContext: context,
		incomingRoute:         incoming,
		outgoingRoute:         outgoing,
		peer:                  nil,

		// Seeded as if this exact chunk was already served for real once - the cache-hit path never
		// touches flow.peer, unlike the cache-miss path's logging, so nil is fine here.
		lastServedLowHash:        lowHash,
		lastServedHighHash:       highHash,
		lastServedActualHighHash: highHash,
		lastServedHeadersMessage: appmessage.NewBlockHeadersMessage(nil),
	}

	done := make(chan error, 1)
	go func() { done <- flow.start() }()

	if err := incoming.Enqueue(appmessage.NewMsgRequstHeaders(lowHash, highHash)); err != nil {
		t.Fatalf("Enqueue: %+v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for outgoing.Length() < 1 {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for the cached headers response")
		}
		time.Sleep(time.Millisecond)
	}
	if err := incoming.Enqueue(&appmessage.MsgRequestNextHeaders{}); err != nil {
		t.Fatalf("Enqueue: %+v", err)
	}

	deadline = time.Now().Add(5 * time.Second)
	for outgoing.Length() < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for DoneHeaders")
		}
		time.Sleep(time.Millisecond)
	}

	incoming.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for the flow to end after closing the incoming route")
	}

	if consensus.getHashesBetweenCalls != 0 {
		t.Fatalf("expected the identical request to be answered from cache with zero GetHashesBetween "+
			"calls, got %d", consensus.getHashesBetweenCalls)
	}

	sawHeaders, sawDone := false, false
	for outgoing.Length() > 0 {
		message, err := outgoing.Dequeue()
		if err != nil {
			t.Fatalf("Dequeue: %+v", err)
		}
		switch message.(type) {
		case *appmessage.BlockHeadersMessage:
			sawHeaders = true
		case *appmessage.MsgDoneHeaders:
			sawDone = true
		}
	}
	if !sawHeaders || !sawDone {
		t.Fatalf("expected both a headers message and DoneHeaders, got headers=%t done=%t", sawHeaders, sawDone)
	}
}
