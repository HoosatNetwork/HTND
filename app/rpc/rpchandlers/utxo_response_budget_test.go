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
