package rpchandlers

import (
	"math"

	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/server/grpcserver"
)

// utxoEntryWireOverhead bounds the encoded size of one UtxosByAddressesEntry beyond its address and
// script hex: field tags and length prefixes, the 64-character transaction ID, and the varint
// index, amount, script version and DAA score at their widest. It is an upper bound, so a response
// sized by it stays under the limit it was sized for.
const utxoEntryWireOverhead = 128

// utxoResponseBudget caps how many coins a UTXO-by-address response carries so that it fits in
// one RPC message.
//
// Without it a response for an address holding millions of coins - a pool's or the dev fee's - was
// built in full, gigabytes of it, and then refused by gRPC for exceeding the message limit: the
// client got nothing and the node had allocated the whole response for it, on every poll. A
// response cut to what fits gives the client coins it can spend, and as it spends or consolidates
// them the ones that did not fit take their place.
type utxoResponseBudget struct {
	remaining int
	truncated bool
}

// newUTXOResponseBudget sizes the budget to the effective RPC message limit, keeping an eighth of it
// for the rest of the response. Protobuf cannot encode a message of 2GiB or more, so a limit set
// above that is held to it.
func newUTXOResponseBudget() *utxoResponseBudget {
	return newUTXOResponseBudgetOfSize(min(grpcserver.RPCMaxMessageSize, math.MaxInt32))
}

func newUTXOResponseBudgetOfSize(messageSize int) *utxoResponseBudget {
	return &utxoResponseBudget{remaining: messageSize - messageSize/8}
}

func utxoEntryWireSize(address string, scriptHex string) int {
	return utxoEntryWireOverhead + len(address) + len(scriptHex)
}

// limitFor returns the limit to read an address's coins with: requested (0 is no limit), lowered to
// the coins that still fit. fits is false when not even one more coin fits.
func (b *utxoResponseBudget) limitFor(address string, scriptHex string, requested uint32) (limit uint32, fits bool) {
	fitting := b.remaining / utxoEntryWireSize(address, scriptHex)
	if fitting <= 0 {
		b.truncated = true
		return 0, false
	}
	if fitting > math.MaxUint32 {
		fitting = math.MaxUint32
	}
	if requested == 0 || uint32(fitting) < requested {
		return uint32(fitting), true
	}
	return requested, true
}

// logTruncatedUTXOResponse says when a response was cut to fit, so an operator whose wallet sees fewer
// coins than it holds can find out why.
func logTruncatedUTXOResponse(budget *utxoResponseBudget, method string, coins int) {
	if !budget.truncated {
		return
	}
	log.Infof("%s response cut to %d coin(s) to fit the RPC message limit of %d bytes; the rest stay "+
		"unlisted until these are spent. Page through them with GetPaginatedUTXOsByAddresses, or "+
		"consolidate the address's coins", method, coins, grpcserver.RPCMaxMessageSize)
}

// spend records that read coins of an address were read with limit, and that kept of them go into the
// response. A read that came back full at a limit the budget lowered may have left coins out.
func (b *utxoResponseBudget) spend(address string, scriptHex string, requested uint32, limit uint32, read int, kept int) {
	b.remaining -= kept * utxoEntryWireSize(address, scriptHex)
	if limit != requested && read >= int(limit) {
		b.truncated = true
	}
}
