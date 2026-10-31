package blockrelay

import (
	"math/big"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/HoosatNetwork/HTND/v2/domain"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/blockheader"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/config"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/router"
)

type parentKnownConsensus struct {
	externalapi.Consensus
	known *externalapi.DomainHash
}

func (c parentKnownConsensus) GetBlockInfo(hash *externalapi.DomainHash) (*externalapi.BlockInfo, error) {
	if hash.Equal(c.known) {
		return &externalapi.BlockInfo{Exists: true, BlockStatus: externalapi.StatusUTXOValid}, nil
	}
	return &externalapi.BlockInfo{}, nil
}

// GetVirtualSelectedParent: past genesis (the parent), so the node is not refused as genesis-only
func (c parentKnownConsensus) GetVirtualSelectedParent() (*externalapi.DomainHash, error) {
	return c.known, nil
}

type parentKnownDomain struct {
	domain.Domain
	consensus parentKnownConsensus
}

func (d parentKnownDomain) Consensus() externalapi.Consensus { return d.consensus }

type orphanTestContext struct {
	RelayInvsContext
	cfg    *config.Config
	dom    parentKnownDomain
	accept bool // what AddOrphan answers
	adds   int
}

func (c *orphanTestContext) Config() *config.Config                { return c.cfg }
func (c *orphanTestContext) Domain() domain.Domain                 { return c.dom }
func (c *orphanTestContext) IsOrphan(*externalapi.DomainHash) bool { return false }
func (c *orphanTestContext) AddOrphan(*externalapi.DomainBlock) (bool, error) {
	c.adds++
	return c.accept, nil
}
func (c *orphanTestContext) GetOrphanRoots(*externalapi.DomainHash) ([]*externalapi.DomainHash, bool, error) {
	return nil, true, nil
}

// TestProcessOrphanRetriesWhenParentsArrived pins the relay side of AddOrphan refusing a block whose parents
// were added while the peer was asked for a block locator: processOrphan reports it, so the flow processes
// the block again instead of leaving it to an IBD. An orphan that AddOrphan keeps is not retried.
func TestProcessOrphanRetriesWhenParentsArrived(t *testing.T) {
	parentHash := externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{1})
	block := &externalapi.DomainBlock{
		Header: blockheader.NewImmutableBlockHeader(1, []externalapi.BlockLevelParents{{parentHash}},
			&externalapi.DomainHash{}, &externalapi.DomainHash{}, &externalapi.DomainHash{},
			0, 0, 1, 0, 0, big.NewInt(0), &externalapi.DomainHash{}),
		PoWHash: "pow",
	}
	cfg := config.DefaultConfig()
	if v := blockVersionForDAAScore(cfg.ActiveNetParams.POWScores, 0); v != 1 {
		t.Skipf("test block is version 1, the default network expects %d at DAA score 0", v)
	}

	for _, accept := range []bool{false, true} {
		ctx := &orphanTestContext{cfg: cfg, dom: parentKnownDomain{consensus: parentKnownConsensus{known: parentHash}}, accept: accept}
		flow := &handleRelayInvsFlow{
			RelayInvsContext: ctx,
			outgoingRoute:    router.NewRoute("outgoing"),
			invChan:          make(chan invRelayBlock, 1),
			locatorChan:      make(chan *appmessage.MsgBlockLocator, 1),
			incomingDone:     make(chan struct{}),
		}
		// the peer's locator names the parent, which this node holds: the block is in orphan resolution range
		flow.locatorChan <- appmessage.NewMsgBlockLocator([]*externalapi.DomainHash{parentHash})

		retry, err := flow.processOrphan(block)
		if err != nil {
			t.Fatalf("processOrphan (AddOrphan accepts: %t): %+v", accept, err)
		}
		if ctx.adds != 1 || retry == accept {
			t.Fatalf("AddOrphan accepts %t: AddOrphan called %d times, retry %t; want 1 call and retry %t",
				accept, ctx.adds, retry, !accept)
		}
	}
}
