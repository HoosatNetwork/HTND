package rpccontext

import (
	"encoding/hex"
	"math"

	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/txscript"
	"github.com/Hoosat-Oy/HTND/util"
	"github.com/pkg/errors"

	"github.com/Hoosat-Oy/HTND/app/appmessage"
	"github.com/Hoosat-Oy/HTND/domain/utxoindex"
)

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
	if value == nil {
		return buffer[:0], ""
	}
	if len(value) > math.MaxInt/2 {
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
