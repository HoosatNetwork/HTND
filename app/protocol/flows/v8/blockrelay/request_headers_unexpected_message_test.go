package blockrelay

import (
	"testing"

	"github.com/HoosatNetwork/HTND/app/appmessage"
	"github.com/HoosatNetwork/HTND/app/protocol/protocolerrors"
	"github.com/HoosatNetwork/HTND/infrastructure/network/netadapter/router"
	"github.com/pkg/errors"
)

// TestHandleRequestHeadersRejectsNextHeadersOutOfTurn pins that a RequestNextHeaders message arriving
// when no header exchange is in progress is a protocol error. The flow's route carries both
// RequestHeaders and RequestNextHeaders, and the flow asserted the first message's type unchecked, so a
// peer sending RequestNextHeaders first panicked the flow goroutine and took the node down.
func TestHandleRequestHeadersRejectsNextHeadersOutOfTurn(t *testing.T) {
	incomingRoute := router.NewRoute("incoming")
	outgoingRoute := router.NewRoute("outgoing")
	if err := incomingRoute.Enqueue(&appmessage.MsgRequestNextHeaders{}); err != nil {
		t.Fatalf("Enqueue: %+v", err)
	}

	// The flow reads its first message before it touches the domain or the peer.
	err := HandleRequestHeaders(nil, incomingRoute, outgoingRoute, nil)
	if err == nil {
		t.Fatalf("expected a protocol error for RequestNextHeaders sent out of turn")
	}
	protocolErr := protocolerrors.ProtocolError{}
	if !errors.As(err, &protocolErr) {
		t.Fatalf("expected a protocol error, got %+v", err)
	}
}
