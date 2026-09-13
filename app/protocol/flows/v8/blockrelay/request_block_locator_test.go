package blockrelay

import (
	"errors"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/app/appmessage"
	"github.com/HoosatNetwork/HTND/app/protocol/protocolerrors"
	"github.com/HoosatNetwork/HTND/domain"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/infrastructure/db/database"
	"github.com/HoosatNetwork/HTND/infrastructure/network/netadapter/router"
	pkgerrors "github.com/pkg/errors"
)

type emptyLocatorConsensus struct {
	externalapi.Consensus
	err error
}

func (c emptyLocatorConsensus) CreateBlockLocatorFromPruningPoint(*externalapi.DomainHash, uint32) (externalapi.BlockLocator, error) {
	return externalapi.BlockLocator{}, c.err
}

type emptyLocatorDomain struct {
	domain.Domain
	err error
}

func (d emptyLocatorDomain) Consensus() externalapi.Consensus {
	return emptyLocatorConsensus{err: d.err}
}

type emptyLocatorContext struct {
	err error
}

func (c emptyLocatorContext) Domain() domain.Domain { return emptyLocatorDomain{err: c.err} }

// TestRequestBlockLocatorEmptyLocatorIsProtocolError pins that when no locator can be built, the flow
// ends with a protocol error, which disconnects the peer. It used to end with nil for an empty locator,
// and with a database not-found error when the high hash had no GHOSTDAG data, which HandleError drops
// silently; either way the connection stayed up with nothing reading its locator requests, and the
// peer waited out a 10-minute timeout.
func TestRequestBlockLocatorEmptyLocatorIsProtocolError(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "empty locator", err: nil},
		{name: "not found", err: pkgerrors.Wrap(database.ErrNotFound, "ghostdag data not found")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			incomingRoute := router.NewRoute("incoming")
			outgoingRoute := router.NewRoute("outgoing")
			if err := incomingRoute.Enqueue(appmessage.NewMsgRequestBlockLocator(&externalapi.DomainHash{}, 5)); err != nil {
				t.Fatalf("Enqueue: %+v", err)
			}

			done := make(chan error, 1)
			go func() {
				done <- HandleRequestBlockLocator(emptyLocatorContext{err: test.err}, incomingRoute, outgoingRoute)
			}()

			select {
			case err := <-done:
				var protocolErr protocolerrors.ProtocolError
				if !errors.As(err, &protocolErr) {
					t.Fatalf("expected a protocol error, got %v", err)
				}
				if database.IsNotFoundError(err) {
					t.Fatalf("the error must not unwrap to not-found, or HandleError drops it: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("HandleRequestBlockLocator neither answered nor failed")
			}
		})
	}
}
