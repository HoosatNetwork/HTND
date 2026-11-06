package blockrelay

import (
	"math/big"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/HoosatNetwork/HTND/v2/domain"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/blockheader"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/config"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/router"
)

type anticoneConsensus struct {
	externalapi.Consensus
	block    *externalapi.DomainBlock
	anticone []*externalapi.DomainHash
}

func (c anticoneConsensus) PruningPointHeaders() ([]externalapi.BlockHeader, error) {
	return []externalapi.BlockHeader{c.block.Header}, nil
}

func (c anticoneConsensus) PruningPointAndItsAnticone() ([]*externalapi.DomainHash, error) {
	return c.anticone, nil
}

func (anticoneConsensus) BlockDAAWindowHashes(*externalapi.DomainHash) ([]*externalapi.DomainHash, error) {
	return nil, nil
}

func (anticoneConsensus) TrustedBlockAssociatedGHOSTDAGDataBlockHashes(*externalapi.DomainHash) ([]*externalapi.DomainHash, error) {
	return nil, nil
}

func (c anticoneConsensus) GetBlock(*externalapi.DomainHash) (*externalapi.DomainBlock, bool, error) {
	return c.block, true, nil
}

type anticoneDomain struct {
	domain.Domain
	consensus externalapi.Consensus
}

func (d anticoneDomain) Consensus() externalapi.Consensus { return d.consensus }

type anticoneContext struct {
	domain domain.Domain
	config *config.Config
}

func (c anticoneContext) Domain() domain.Domain  { return c.domain }
func (c anticoneContext) Config() *config.Config { return c.config }

// TestPruningPointAnticoneServingIsNotBlockedByAStalledSyncee pins that a syncee which stops asking for the next
// batch of the pruning point anticone does not block other syncees. The flow held a node-wide busy flag while it
// waited, without a timeout, for the syncee's next request, so one peer that requested the anticone and then went
// quiet made every other peer's request fail as "busy" for as long as it stayed connected.
func TestPruningPointAnticoneServingIsNotBlockedByAStalledSyncee(t *testing.T) {
	header := blockheader.NewImmutableBlockHeader(1, nil, &externalapi.DomainHash{}, &externalapi.DomainHash{},
		&externalapi.DomainHash{}, 0, 0, 0, 0, 0, big.NewInt(0), &externalapi.DomainHash{})
	anticone := make([]*externalapi.DomainHash, getIBDBatchSize()+1)
	for i := range anticone {
		anticone[i] = &externalapi.DomainHash{}
	}
	context := anticoneContext{
		domain: anticoneDomain{consensus: anticoneConsensus{
			block:    &externalapi.DomainBlock{Header: header},
			anticone: anticone,
		}},
		config: &config.Config{Flags: &config.Flags{NetworkFlags: config.NetworkFlags{ActiveNetParams: &dagconfig.MainnetParams}}},
	}
	// Pruning points, trusted data, then one full batch before the flow waits for the next request.
	firstBatchMessages := 2 + getIBDBatchSize()

	serve := func(name string) (*router.Route, *router.Route, <-chan error) {
		incoming := router.NewRoute(name + " incoming")
		outgoing := router.NewRouteWithCapacity(name+" outgoing", 10*firstBatchMessages)
		if err := incoming.Enqueue(appmessage.NewMsgRequestPruningPointAndItsAnticone()); err != nil {
			t.Fatalf("Enqueue: %+v", err)
		}
		done := make(chan error, 1)
		go func() { done <- HandlePruningPointAndItsAnticoneRequests(context, incoming, outgoing, nil) }()
		return incoming, outgoing, done
	}
	waitForFirstBatch := func(name string, outgoing *router.Route, done <-chan error) {
		deadline := time.Now().Add(10 * time.Second)
		for outgoing.Length() < firstBatchMessages {
			select {
			case err := <-done:
				t.Fatalf("%s: the flow ended before sending its first batch (%d of %d messages): %v",
					name, outgoing.Length(), firstBatchMessages, err)
			default:
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: sent %d of %d messages of the first batch", name, outgoing.Length(), firstBatchMessages)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}

	stalledIncoming, stalledOutgoing, stalledDone := serve("stalled syncee")
	defer stalledIncoming.Close()
	waitForFirstBatch("stalled syncee", stalledOutgoing, stalledDone)

	// The stalled syncee never sends RequestNextPruningPointAndItsAnticoneBlocks. Another syncee must still be served.
	otherIncoming, otherOutgoing, otherDone := serve("other syncee")
	defer otherIncoming.Close()
	waitForFirstBatch("other syncee", otherOutgoing, otherDone)
}
