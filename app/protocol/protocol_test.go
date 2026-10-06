package protocol

import (
	"testing"

	"github.com/HoosatNetwork/HTND/v2/app/protocol/protocolerrors"
	"github.com/pkg/errors"
)

func TestRegisterFlowsForUnsupportedProtocolReturnsDisconnectError(t *testing.T) {
	flows, err := registerFlowsForProtocol(nil, nil, nil, nil, nil, 999)
	if flows != nil {
		t.Fatalf("unsupported protocol registered %d flows", len(flows))
	}
	var protocolErr protocolerrors.ProtocolError
	if !errors.As(err, &protocolErr) {
		t.Fatalf("unsupported protocol returned %T, want ProtocolError", err)
	}
	if protocolErr.ShouldBan {
		t.Fatal("unsupported protocol version should disconnect without banning")
	}
}
