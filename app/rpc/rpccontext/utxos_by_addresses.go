package rpccontext

import (
	"encoding/hex"
	"math"

	"github.com/HoosatNetwork/HTND/domain/consensus/utils/txscript"
	"github.com/HoosatNetwork/HTND/util"
	"github.com/pkg/errors"

	"github.com/HoosatNetwork/HTND/app/appmessage"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/utxoindex"
)

// virtualUTXOSource is the part of consensus this file needs: what virtual's UTXO set holds.
type virtualUTXOSource interface {
	GetVirtualUTXOEntries(outpoints []*externalapi.DomainOutpoint) ([]externalapi.UTXOEntry, error)
}

// FilterUTXOPairsAgainstVirtual withholds index entries whose coin virtual's UTXO set does not hold,
// and returns the rest carrying virtual's own entry. It reports how many were withheld.
//
// The UTXO index is a secondary structure maintained by applying diffs, and it can disagree with the
// set consensus spends from. Both kinds of disagreement reach the spender as a broken coin. An
// outpoint the index still lists but consensus does not hold is one a wallet will build a transaction
// around, and that transaction is refused by every node, this one included, for a missing input. And
// an entry whose contents drifted - a coin's BlockDAAScore changes when a different block takes over
// merging it - misstates the coin a spender is being handed, while the stamp is part of what the
// signed transaction commits to.
//
// So consensus answers both questions here, and the index is left to do the one thing only it can:
// say which outpoints belong to an address.
func FilterUTXOPairsAgainstVirtual(source virtualUTXOSource, pairs []utxoindex.UTXOPair) ([]utxoindex.UTXOPair, int, error) {
	if len(pairs) == 0 {
		return pairs, 0, nil
	}
	outpoints := make([]*externalapi.DomainOutpoint, len(pairs))
	for i := range pairs {
		outpoints[i] = &pairs[i].Outpoint
	}
	entries, err := source.GetVirtualUTXOEntries(outpoints)
	if err != nil {
		return nil, 0, err
	}
	if len(entries) != len(pairs) {
		return nil, 0, errors.Errorf("consensus answered for %d outpoints, %d were asked about", len(entries), len(pairs))
	}

	kept := pairs[:0]
	withheld := 0
	for i, entry := range entries {
		if entry == nil {
			withheld++
			continue
		}
		pair := pairs[i]
		pair.Entry = entry
		kept = append(kept, pair)
	}
	return kept, withheld, nil
}

// ConvertUTXOOutpointEntryPairToUTXOsByAddressesEntry converts
// a UTXOOutpointEntryPair to a slice of UTXOsByAddressesEntry
func ConvertUTXOOutpointEntryPairToUTXOsByAddressesEntry(address string, script *appmessage.RPCScriptPublicKey, pair utxoindex.UTXOPair) *appmessage.UTXOsByAddressesEntry {
	// Compute scriptHex once per address (all UTXOs for this address share the same ScriptPublicKey)

	return &appmessage.UTXOsByAddressesEntry{
		Address: address,
		Outpoint: &appmessage.RPCOutpoint{
			TransactionID: pair.Outpoint.TransactionID.String(),
			Index:         pair.Outpoint.Index,
		},
		UTXOEntry: &appmessage.RPCUTXOEntry{
			Amount:          pair.Entry.Amount(),
			ScriptPublicKey: script,
			BlockDAAScore:   pair.Entry.BlockDAAScore(),
			IsCoinbase:      pair.Entry.IsCoinbase(),
		},
	}
}

func encodeHexString(buffer []byte, value []byte) ([]byte, string) {
	return encodeHexStringWithMaxValueLen(buffer, value, math.MaxInt/2)
}

func encodeHexStringWithMaxValueLen(buffer []byte, value []byte, maxValueLen int) ([]byte, string) {
	if value == nil {
		return buffer[:0], ""
	}
	if maxValueLen < 0 {
		return buffer[:0], ""
	}
	if len(value) > maxValueLen {
		return buffer[:0], ""
	}
	needed := hex.EncodedLen(len(value))
	if needed == 0 {
		return buffer[:0], ""
	}
	if cap(buffer) < needed {
		buffer = make([]byte, needed)
	} else {
		buffer = buffer[:needed]
	}
	hex.Encode(buffer, value)
	return buffer, string(buffer)
}

// ConvertAddressStringsToUTXOsChangedNotificationAddresses converts address strings
// to UTXOsChangedNotificationAddresses
func (ctx *Context) ConvertAddressStringsToUTXOsChangedNotificationAddresses(
	addressStrings []string,
) ([]*UTXOsChangedNotificationAddress, error) {
	addresses := make([]*UTXOsChangedNotificationAddress, len(addressStrings))
	var reusableHexBuffer []byte
	for i, addressString := range addressStrings {
		address, err := util.DecodeAddress(addressString, ctx.Config.ActiveNetParams.Prefix)
		if err != nil {
			return nil, errors.Errorf("Could not decode address '%s': %s", addressString, err)
		}
		scriptPublicKey, err := txscript.PayToAddrScript(address)
		if err != nil {
			return nil, errors.Errorf("Could not create a scriptPublicKey for address '%s': %s", addressString, err)
		}
		var scriptHex string
		reusableHexBuffer, scriptHex = encodeHexString(reusableHexBuffer, scriptPublicKey.Script)
		scriptPublicKeyString := utxoindex.ScriptPublicKeyString(scriptHex)
		addresses[i] = &UTXOsChangedNotificationAddress{
			Address:               addressString,
			ScriptPublicKeyString: scriptPublicKeyString,
		}
	}
	return addresses, nil
}
