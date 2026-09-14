package protowire

import (
	"testing"

	"github.com/HoosatNetwork/HTND/app/appmessage"
)

// TestStopNotifyingPruningPointUTXOSetOverrideResponseWire pins that the stop response can be put on the wire. It had
// no encoder, so FromAppMessage failed with an unknown message type and the server could not answer a stop request.
func TestStopNotifyingPruningPointUTXOSetOverrideResponseWire(t *testing.T) {
	response := appmessage.NewStopNotifyingPruningPointUTXOSetOverrideResponseMessage()
	response.Error = appmessage.RPCErrorf("stop failed")

	wire, err := FromAppMessage(response)
	if err != nil {
		t.Fatalf("FromAppMessage: %+v", err)
	}
	back, err := wire.ToAppMessage()
	if err != nil {
		t.Fatalf("ToAppMessage: %+v", err)
	}
	decoded, ok := back.(*appmessage.StopNotifyingPruningPointUTXOSetOverrideResponseMessage)
	if !ok {
		t.Fatalf("round trip produced %T", back)
	}
	if decoded.Error == nil || decoded.Error.Message != "stop failed" {
		t.Fatalf("round trip lost the error: %+v", decoded.Error)
	}
}
