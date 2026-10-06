package blockrelay

import (
	"math/big"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/HoosatNetwork/HTND/v2/domain"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/blockheader"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/router"
)

type locatorConsensus struct {
	externalapi.Consensus
	block     *externalapi.DomainBlock
	getBlocks *atomic.Int64
}

func (locatorConsensus) IsNearlySynced() (bool, error) { return true, nil }

func (locatorConsensus) GetBlockInfo(*externalapi.DomainHash) (*externalapi.BlockInfo, error) {
	return &externalapi.BlockInfo{Exists: true, BlockStatus: externalapi.StatusUTXOValid}, nil
}

func (c locatorConsensus) GetBlock(*externalapi.DomainHash) (*externalapi.DomainBlock, bool, error) {
	c.getBlocks.Add(1)
	return c.block, true, nil
}

func (locatorConsensus) IsInSelectedParentChainOf(*externalapi.DomainHash, *externalapi.DomainHash) (bool, error) {
	return false, nil
}

type locatorContext struct {
	domain domain.Domain
}

func (c locatorContext) Domain() domain.Domain { return c.domain }
func (locatorContext) IsIBDRunning() bool      { return false }

// TestIBDBlockLocatorWorkIsBounded pins that the node reads at most MaxBlockLocatorsPerMsg blocks for one
// IBDBlockLocator message. The message's hash count is not limited on the wire, and every hash cost a full block read
// and a selected-chain check under the consensus lock, so a peer repeating a known block that is not on the target's
// selected chain made the node do millions of block reads for a single message. Honest locators are logarithmic in
// the chain length, far below the bound.
func TestIBDBlockLocatorWorkIsBounded(t *testing.T) {
	header := blockheader.NewImmutableBlockHeader(1, nil, &externalapi.DomainHash{}, &externalapi.DomainHash{},
		&externalapi.DomainHash{}, 0, 0, 0, 0, 0, big.NewInt(0), &externalapi.DomainHash{})
	getBlocks := &atomic.Int64{}
	context := locatorContext{domain: anticoneDomain{consensus: locatorConsensus{
		block:     &externalapi.DomainBlock{Header: header, PoWHash: "pow"},
		getBlocks: getBlocks,
	}}}

	const hashCount = 10_000
	hashes := make([]*externalapi.DomainHash, hashCount)
	for i := range hashes {
		hashes[i] = &externalapi.DomainHash{}
	}
	incoming := router.NewRoute("incoming")
	outgoing := router.NewRoute("outgoing")
	if err := incoming.Enqueue(appmessage.NewMsgIBDBlockLocator(&externalapi.DomainHash{}, hashes)); err != nil {
		t.Fatalf("Enqueue: %+v", err)
	}
	done := make(chan error, 1)
	go func() { done <- HandleIBDBlockLocator(context, incoming, outgoing, nil) }()
	defer incoming.Close()

	deadline := time.Now().Add(10 * time.Second)
	for outgoing.Length() == 0 {
		select {
		case err := <-done:
			t.Fatalf("the flow ended without answering: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("no answer to the locator after %d block reads", getBlocks.Load())
		}
		time.Sleep(5 * time.Millisecond)
	}
	message, err := outgoing.Dequeue()
	if err != nil {
		t.Fatalf("Dequeue: %+v", err)
	}
	if _, ok := message.(*appmessage.MsgIBDBlockLocatorHighestHashNotFound); !ok {
		t.Fatalf("expected highest hash not found, got %s", message.Command())
	}
	if reads := getBlocks.Load(); reads > appmessage.MaxBlockLocatorsPerMsg {
		t.Fatalf("read %d blocks for one locator of %d hashes, want at most %d",
			reads, hashCount, appmessage.MaxBlockLocatorsPerMsg)
	}
}
