package flowcontext

import (
	"errors"
	"testing"

	"github.com/HoosatNetwork/HTND/app/protocol/protocolerrors"
	"github.com/HoosatNetwork/HTND/domain/consensus/ruleerrors"
)

// TestHandleErrorBansOnlyForPeerFaults pins which flow errors ban the peer. An error that is neither a protocol
// error, a consensus rule error nor malformed wire data is usually this node's own failure - a database I/O error,
// a full disk - and it recurs with every peer. It used to become a banning protocol error, so a local fault made
// the node ban its honest peers one after another. It must still end the flow, but without a ban.
func TestHandleErrorBansOnlyForPeerFaults(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		shouldBan bool
	}{
		{name: "local fault", err: errors.New("pebble: disk I/O error"), shouldBan: false},
		{name: "consensus rule violation", err: ruleerrors.ErrInvalidPoW, shouldBan: true},
		{name: "malformed wire data", err: errors.New("proto: cannot parse invalid wire-format data"), shouldBan: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			errChan := make(chan error, 1)
			var isStopping uint32
			(&FlowContext{}).HandleError(test.err, "TestFlow", &isStopping, errChan)

			select {
			case handled := <-errChan:
				protocolErr := protocolerrors.ProtocolError{}
				if !errors.As(handled, &protocolErr) {
					t.Fatalf("expected a protocol error, got %+v", handled)
				}
				if protocolErr.ShouldBan != test.shouldBan {
					t.Fatalf("ShouldBan = %t, want %t (error: %v)", protocolErr.ShouldBan, test.shouldBan, handled)
				}
			default:
				t.Fatalf("HandleError did not forward the error, so the peer would not be disconnected")
			}
		})
	}
}
