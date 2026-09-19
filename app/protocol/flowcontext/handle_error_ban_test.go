package flowcontext

import (
	"errors"
	"testing"

	"github.com/HoosatNetwork/HTND/app/protocol/protocolerrors"
	"github.com/HoosatNetwork/HTND/domain/consensus/ruleerrors"
)

// TestHandleErrorBansOnlyForPeerFaults pins which flow errors ban the peer. An error that is neither a protocol
// error nor a consensus rule error is usually this node's own failure - a database I/O error, a full disk - and
// it recurs with every peer. It used to become a banning protocol error, so a local fault made the node ban its
// honest peers one after another. It must still end the flow, but without a ban.
//
// The "wire-format"/"protobuf ... parse" text match is HTN-168: flows never decode protobuf bytes themselves
// (the on-wire message is already decoded, and banned separately, before it reaches a flow), so an error with
// this text reaching HandleError is a local failure too - e.g. a corrupted local store record's raw UnmarshalVT
// error happens to contain the same wording. It used to ban on this text unconditionally, for exactly the same
// "local fault made the node ban honest peers" reason as the case above.
func TestHandleErrorBansOnlyForPeerFaults(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		shouldBan bool
	}{
		{name: "local fault", err: errors.New("pebble: disk I/O error"), shouldBan: false},
		{name: "consensus rule violation", err: ruleerrors.ErrInvalidPoW, shouldBan: true},
		{name: "wire-format-looking local failure", err: errors.New("proto: cannot parse invalid wire-format data"), shouldBan: false},
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
