package blockrelay

import (
	"errors"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/HoosatNetwork/HTND/v2/app/protocol/protocolerrors"
	"github.com/HoosatNetwork/HTND/v2/domain"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/router"
)

type missingBlockConsensus struct {
	externalapi.Consensus
}

func (missingBlockConsensus) GetBlock(*externalapi.DomainHash) (*externalapi.DomainBlock, bool, error) {
	return nil, false, nil
}

func (missingBlockConsensus) GetBlockEvenIfHeaderOnly(*externalapi.DomainHash) (*externalapi.DomainBlock, error) {
	return nil, database.ErrNotFound
}

type missingBlockDomain struct {
	domain.Domain
}

func (missingBlockDomain) Consensus() externalapi.Consensus { return missingBlockConsensus{} }

type missingBlockContext struct{}

func (missingBlockContext) Domain() domain.Domain { return missingBlockDomain{} }

// TestBlockRequestServersFailOnMissingBlock pins that the relay and IBD block request servers end with
// a protocol error, which disconnects the peer, when they cannot serve a requested block. They used to
// only set a flag inside the worker goroutine: nothing was sent, the flow carried on (or stopped with
// nil), and the requesting peer waited out its timeout.
func TestBlockRequestServersFailOnMissingBlock(t *testing.T) {
	tests := []struct {
		name    string
		request appmessage.Message
		run     func(incoming, outgoing *router.Route) error
	}{
		{
			name:    "relay",
			request: appmessage.NewMsgRequestRelayBlocks([]*externalapi.DomainHash{{}}),
			run: func(incoming, outgoing *router.Route) error {
				return HandleRelayBlockRequests(missingBlockContext{}, incoming, outgoing, nil)
			},
		},
		{
			name:    "ibd",
			request: appmessage.NewMsgRequestIBDBlocks([]*externalapi.DomainHash{{}}),
			run: func(incoming, outgoing *router.Route) error {
				return HandleIBDBlockRequests(missingBlockContext{}, incoming, outgoing, nil)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			incomingRoute := router.NewRoute("incoming")
			outgoingRoute := router.NewRoute("outgoing")
			if err := incomingRoute.Enqueue(test.request); err != nil {
				t.Fatalf("Enqueue: %+v", err)
			}

			done := make(chan error, 1)
			go func() { done <- test.run(incomingRoute, outgoingRoute) }()

			select {
			case err := <-done:
				var protocolErr protocolerrors.ProtocolError
				if !errors.As(err, &protocolErr) {
					t.Fatalf("expected a protocol error, got %v", err)
				}
				if protocolErr.ShouldBan {
					t.Fatalf("a block this node cannot serve is not grounds for banning the requester")
				}
				if database.IsNotFoundError(err) {
					t.Fatalf("the error must not unwrap to not-found, or HandleError drops it: %v", err)
				}
			case <-time.After(5 * time.Second):
				incomingRoute.Close()
				t.Fatalf("the server neither served the block nor failed")
			}
		})
	}
}
