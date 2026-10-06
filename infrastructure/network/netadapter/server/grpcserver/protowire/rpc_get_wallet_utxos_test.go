package protowire

import (
	"reflect"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"google.golang.org/protobuf/proto"
)

// roundTripOverTheWire converts message to its wire form, serializes it, and converts it back, as a
// client and the node do on either side of the RPC connection.
func roundTripOverTheWire(t *testing.T, message appmessage.Message) appmessage.Message {
	t.Helper()
	wireMessage, err := FromAppMessage(message)
	if err != nil {
		t.Fatalf("FromAppMessage: %+v", err)
	}
	serialized, err := proto.Marshal(wireMessage)
	if err != nil {
		t.Fatalf("Marshal: %+v", err)
	}
	received := &HoosatdMessage{}
	if err := proto.Unmarshal(serialized, received); err != nil {
		t.Fatalf("Unmarshal: %+v", err)
	}
	converted, err := received.ToAppMessage()
	if err != nil {
		t.Fatalf("ToAppMessage: %+v", err)
	}
	return converted
}

// TestGetWalletUTXOsRoundTrips pins that every field of the request and the response survives the wire,
// and that each arrives as its own command so the client routes the response to the waiting request.
func TestGetWalletUTXOsRoundTrips(t *testing.T) {
	request := appmessage.NewGetWalletUTXOsRequestMessage([]string{"xpub-a", "xpub-b"}, 2, true, 50, 7)
	if got := roundTripOverTheWire(t, request); !reflect.DeepEqual(got, request) {
		t.Fatalf("request changed across the wire:\n got %+v\nwant %+v", got, request)
	}

	response := appmessage.NewGetWalletUTXOsResponseMessage([]*appmessage.WalletUTXOEntry{{
		Address:  "hoosat:qq",
		Outpoint: &appmessage.RPCOutpoint{TransactionID: "aa", Index: 3},
		UTXOEntry: &appmessage.RPCUTXOEntry{
			Amount:          12345,
			ScriptPublicKey: &appmessage.RPCScriptPublicKey{Version: 0, Script: "20ab"},
			BlockDAAScore:   99,
			IsCoinbase:      true,
		},
		DerivationPath: "m/1/4",
	}}, 104, 101, true)
	got := roundTripOverTheWire(t, response)
	if got.Command() != appmessage.CmdGetWalletUTXOsResponseMessage {
		t.Fatalf("the response arrived as %s", got.Command())
	}
	if !reflect.DeepEqual(got, response) {
		t.Fatalf("response changed across the wire:\n got %+v\nwant %+v", got, response)
	}

	errorResponse := &appmessage.GetWalletUTXOsResponseMessage{Error: appmessage.RPCErrorf("bad request")}
	if got := roundTripOverTheWire(t, errorResponse).(*appmessage.GetWalletUTXOsResponseMessage); got.Error == nil ||
		got.Error.Message != "bad request" {
		t.Fatalf("the error response arrived as %+v", got)
	}
}
