package protowire

import (
	"testing"

	"github.com/HoosatNetwork/HTND/app/appmessage"
)

// TestGetInfoResponseRoundTrip pins every field of GetInfo across the wire in both directions.
//
// It exists because this repo has already shipped a bug of exactly this shape: a value that
// converted correctly in one direction and not the other, which reached users as a transaction
// status reported by number instead of by name. A field that is added to the proto and the app
// message but forgotten in one of the two converters produces a silent wrong answer, not a
// compile error - so the round trip is asserted rather than the field list.
func TestGetInfoResponseRoundTrip(t *testing.T) {
	for _, isUTXOSetVerified := range []bool{true, false} {
		original := &appmessage.GetInfoResponseMessage{
			P2PID:             "p2p-id",
			MempoolSize:       17,
			ServerVersion:     "2.16.0-test",
			IsUtxoIndexed:     true,
			IsSynced:          true,
			IsUtxoSetVerified: isUTXOSetVerified,
		}

		wire := &HoosatdMessage_GetInfoResponse{}
		if err := wire.fromAppMessage(original); err != nil {
			t.Fatalf("fromAppMessage: %+v", err)
		}
		if wire.GetInfoResponse.IsUtxoSetVerified != isUTXOSetVerified {
			t.Errorf("isUtxoSetVerified=%t was dropped on the way to the wire", isUTXOSetVerified)
		}

		converted, err := wire.toAppMessage()
		if err != nil {
			t.Fatalf("toAppMessage: %+v", err)
		}
		roundTripped, ok := converted.(*appmessage.GetInfoResponseMessage)
		if !ok {
			t.Fatalf("toAppMessage returned %T", converted)
		}

		if *roundTripped != *original {
			t.Errorf("round trip changed the message.\n before: %+v\n  after: %+v",
				*original, *roundTripped)
		}
	}
}

// TestGetInfoUnverifiedIsTheDefault pins the fail-safe direction. A server too old to know about
// the field leaves it unset, which arrives as false - and false has to mean "do not trust this
// node's UTXO set", never "trusted". Had the boolean been named the other way round, every
// unupgraded node on the network would have advertised itself as healthy.
func TestGetInfoUnverifiedIsTheDefault(t *testing.T) {
	wire := &HoosatdMessage_GetInfoResponse{
		GetInfoResponse: &GetInfoResponseMessage{P2PId: "p2p-id"},
	}
	converted, err := wire.toAppMessage()
	if err != nil {
		t.Fatalf("toAppMessage: %+v", err)
	}
	message := converted.(*appmessage.GetInfoResponseMessage)
	if message.IsUtxoSetVerified {
		t.Error("a response with the field unset must read as unverified")
	}
}
