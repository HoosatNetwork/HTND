package blockrelay

import (
	"errors"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/HoosatNetwork/HTND/v2/app/protocol/flowcontext"
	"github.com/HoosatNetwork/HTND/v2/app/protocol/protocolerrors"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/router"
)

type requestedBlocksContext struct {
	RelayInvsContext
	shared *flowcontext.SharedRequestedBlocks
}

func (c requestedBlocksContext) SharedRequestedBlocks() *flowcontext.SharedRequestedBlocks {
	return c.shared
}

func newRelayInvsReaderTestFlow() *handleRelayInvsFlow {
	return &handleRelayInvsFlow{
		RelayInvsContext: requestedBlocksContext{shared: flowcontext.NewSharedRequestedBlocks()},
		incomingRoute:    router.NewRoute("incoming"),
		outgoingRoute:    router.NewRoute("outgoing"),
		invChan:          make(chan invRelayBlock, 2048),
		blockChan:        make(chan *appmessage.MsgBlock, 2048),
		locatorChan:      make(chan *appmessage.MsgBlockLocator, 2048),
		incomingDone:     make(chan struct{}),
	}
}

// TestRelayInvsRefusesUnsolicitedBlocks: a peer sends blocks this node never asked for. The incoming
// reader used to queue every one of them in blockChan (2048 deep, each up to the P2P message ceiling)
// and keep the connection, holding them in memory until a later request happened to read one. It must
// drop the peer at the first unsolicited block and keep none.
func TestRelayInvsRefusesUnsolicitedBlocks(t *testing.T) {
	flow := newRelayInvsReaderTestFlow()
	defer flow.incomingRoute.Close()

	block := appmessage.DomainBlockToMsgBlock(dagconfig.MainnetParams.GenesisBlock)
	const unsolicited = 100
	for range unsolicited {
		if err := flow.incomingRoute.Enqueue(block); err != nil {
			t.Fatalf("Enqueue: %+v", err)
		}
	}
	flow.startIncomingReader()

	select {
	case <-flow.incomingDone:
	case <-time.After(5 * time.Second):
		t.Fatalf("the peer was kept after sending %d unsolicited blocks; %d of them are buffered",
			unsolicited, len(flow.blockChan))
	}
	var protocolErr protocolerrors.ProtocolError
	if err := flow.getIncomingErr(); !errors.As(err, &protocolErr) || !protocolErr.ShouldBan {
		t.Errorf("expected a banning protocol error, got: %v", err)
	}
	if buffered := len(flow.blockChan); buffered != 0 {
		t.Errorf("%d unsolicited blocks are still buffered", buffered)
	}
}

// TestRelayInvsAcceptsRequestedBlock pins that a block answering this node's own request still gets
// through the reader.
func TestRelayInvsAcceptsRequestedBlock(t *testing.T) {
	flow := newRelayInvsReaderTestFlow()
	defer flow.incomingRoute.Close()
	flow.startIncomingReader()

	domainBlock := dagconfig.MainnetParams.GenesisBlock
	hash := consensushashing.BlockHash(domainBlock)
	done := make(chan error, 1)
	go func() {
		block, _, err := flow.requestBlock(hash)
		if err == nil && !consensushashing.BlockHash(block).Equal(hash) {
			err = errors.New("requestBlock returned a different block")
		}
		done <- err
	}()

	if _, err := flow.outgoingRoute.DequeueWithTimeout(5 * time.Second); err != nil {
		t.Fatalf("expected a RequestRelayBlocks message: %+v", err)
	}
	if err := flow.incomingRoute.Enqueue(appmessage.DomainBlockToMsgBlock(domainBlock)); err != nil {
		t.Fatalf("Enqueue: %+v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("requestBlock: %+v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the requested block never reached requestBlock")
	}
}
