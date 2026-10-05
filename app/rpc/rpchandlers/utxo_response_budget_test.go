package rpchandlers

import (
	"strings"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/server/grpcserver/protowire"
	"google.golang.org/protobuf/proto"
)

// utxoEntryWireOverhead must be an upper bound, or a response sized by it can still exceed the message
// limit. Encode an entry with every number at its widest and check it fits.
func TestUTXOEntryWireOverheadBoundsTheEncodedEntry(t *testing.T) {
	address := "hoosat:" + strings.Repeat("q", 63)
	scriptHex := strings.Repeat("ab", 35)
	entry := &appmessage.UTXOsByAddressesEntry{
		Address:  address,
		Outpoint: &appmessage.RPCOutpoint{TransactionID: strings.Repeat("f", 64), Index: ^uint32(0)},
		UTXOEntry: &appmessage.RPCUTXOEntry{
			Amount:          ^uint64(0),
			ScriptPublicKey: &appmessage.RPCScriptPublicKey{Script: scriptHex, Version: ^uint16(0)},
			BlockDAAScore:   ^uint64(0),
			IsCoinbase:      true,
		},
	}
	response := appmessage.NewGetUTXOsByAddressesResponseMessage([]*appmessage.UTXOsByAddressesEntry{entry, entry})
	message, err := protowire.FromAppMessage(response)
	if err != nil {
		t.Fatalf("FromAppMessage: %+v", err)
	}
	empty, err := protowire.FromAppMessage(appmessage.NewGetUTXOsByAddressesResponseMessage(nil))
	if err != nil {
		t.Fatalf("FromAppMessage: %+v", err)
	}
	perEntry := (proto.Size(message) - proto.Size(empty)) / 2
	if bound := utxoEntryWireSize(address, scriptHex); perEntry > bound {
		t.Fatalf("an entry encodes to %d bytes, more than the %d utxoEntryWireSize allows for it", perEntry, bound)
	}
}

func TestUTXOResponseBudgetCapsTheResponse(t *testing.T) {
	address, scriptHex := "hoosat:addr", "51"
	entrySize := utxoEntryWireSize(address, scriptHex)
	budget := newUTXOResponseBudgetOfSize(80 * entrySize) // 70 entries after the eighth kept back

	limit, fits := budget.limitFor(address, scriptHex, 0)
	if !fits || limit != 70 {
		t.Fatalf("with no requested limit, got limit %d (fits %t), want the 70 that fit", limit, fits)
	}
	if limit, _ := budget.limitFor(address, scriptHex, 10); limit != 10 {
		t.Fatalf("a requested limit below what fits must be kept, got %d", limit)
	}

	// The first address fills the response; the read came back full at the lowered limit.
	budget.spend(address, scriptHex, 0, 70, 70, 70)
	if !budget.truncated {
		t.Fatalf("a read that came back full at a limit the budget lowered must count as truncated")
	}
	if _, fits := budget.limitFor(address, scriptHex, 0); fits {
		t.Fatalf("no room is left, so the next address must not fit")
	}
}

func TestUTXOResponseBudgetDoesNotReportASmallResponseTruncated(t *testing.T) {
	address, scriptHex := "hoosat:addr", "51"
	budget := newUTXOResponseBudgetOfSize(1 << 20)
	limit, _ := budget.limitFor(address, scriptHex, 0)
	budget.spend(address, scriptHex, 0, limit, 3, 3)
	if budget.truncated {
		t.Fatalf("an address with 3 coins read under a limit of %d is complete", limit)
	}
}
