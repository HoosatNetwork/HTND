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

			CirculatingSompiSupply:     830217308434142827,
			ReferenceSompiSupply:       640111049839169443,
			ReferenceSupplyDescription: "balances-20260801-00 (2026-08-01 00:00 UTC)",
		}

		wire := &HoosatdMessage_GetInfoResponse{}
		if err := wire.fromAppMessage(original); err != nil {
			t.Fatalf("fromAppMessage: %+v", err)
		}
		if wire.GetInfoResponse.IsUtxoSetVerified != isUTXOSetVerified {
			t.Errorf("isUtxoSetVerified=%t was dropped on the way to the wire", isUTXOSetVerified)
		}
		if wire.GetInfoResponse.CirculatingSompiSupply != original.CirculatingSompiSupply {
			t.Error("circulatingSompiSupply was dropped on the way to the wire")
		}
		if wire.GetInfoResponse.ReferenceSompiSupply != original.ReferenceSompiSupply {
			t.Error("referenceSompiSupply was dropped on the way to the wire")
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

// TestGetInfoSupplyIsSelfDescribing pins that a response carries the reference it was measured
// against, not only the difference from it.
//
// The point of the figure is comparing nodes. If two nodes are built from revisions whose reference
// snapshots differ, comparing their growth numbers is meaningless - and would look fine. Sending the
// reference makes that mismatch visible instead of silent.
func TestGetInfoSupplyIsSelfDescribing(t *testing.T) {
	wire := &HoosatdMessage_GetInfoResponse{}
	err := wire.fromAppMessage(&appmessage.GetInfoResponseMessage{
		P2PID:                      "p2p-id",
		CirculatingSompiSupply:     830217308434142827,
		ReferenceSompiSupply:       640111049839169443,
		ReferenceSupplyDescription: "balances-20260801-00 (2026-08-01 00:00 UTC)",
	})
	if err != nil {
		t.Fatalf("fromAppMessage: %+v", err)
	}
	converted, err := wire.toAppMessage()
	if err != nil {
		t.Fatalf("toAppMessage: %+v", err)
	}
	message := converted.(*appmessage.GetInfoResponseMessage)
	if message.ReferenceSompiSupply == 0 {
		t.Error("the reference must travel with the measurement")
	}
	if message.ReferenceSupplyDescription == "" {
		t.Error("the reference should name the snapshot it came from")
	}
	if message.CirculatingSompiSupply <= message.ReferenceSompiSupply {
		t.Error("test fixture no longer represents a node that has grown since the snapshot")
	}
}

// TestGetInfoSupplyAbsentWithoutIndex pins that a node with no utxoindex reports zero rather than a
// number it cannot stand behind.
func TestGetInfoSupplyAbsentWithoutIndex(t *testing.T) {
	wire := &HoosatdMessage_GetInfoResponse{
		GetInfoResponse: &GetInfoResponseMessage{P2PId: "p2p-id"},
	}
	converted, err := wire.toAppMessage()
	if err != nil {
		t.Fatalf("toAppMessage: %+v", err)
	}
	message := converted.(*appmessage.GetInfoResponseMessage)
	if message.CirculatingSompiSupply != 0 {
		t.Error("a node without an index must report no supply, not a stale one")
	}
}
