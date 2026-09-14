package flowcontext

import (
	"testing"

	"github.com/HoosatNetwork/HTND/app/protocol/protocolerrors"
	"github.com/HoosatNetwork/HTND/infrastructure/db/database"
	"github.com/pkg/errors"
)

// TestHandleErrorDisconnectsOnNotFound pins that a flow ending with a database not-found error reaches
// errChan as a non-banning protocol error, so the peer is disconnected. The error used to be dropped:
// the flow had already exited, the peer stayed connected, and nothing read that flow's route again.
func TestHandleErrorDisconnectsOnNotFound(t *testing.T) {
	f := &FlowContext{}
	isStopping := uint32(0)
	errChan := make(chan error, 1)

	f.HandleError(errors.Wrap(database.ErrNotFound, "block not found"), "TestFlow", &isStopping, errChan)

	select {
	case err := <-errChan:
		var protocolErr protocolerrors.ProtocolError
		if !errors.As(err, &protocolErr) {
			t.Fatalf("expected a protocol error, got %v", err)
		}
		if protocolErr.ShouldBan {
			t.Fatalf("a missing database entry is not grounds for banning the peer")
		}
	default:
		t.Fatalf("a not-found error from a flow was dropped instead of disconnecting the peer")
	}
}
