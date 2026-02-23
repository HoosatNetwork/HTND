package rpccontext

import (
	"encoding/hex"

	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/txscript"
	"github.com/Hoosat-Oy/HTND/util"
	"github.com/pkg/errors"

	"github.com/Hoosat-Oy/HTND/app/appmessage"
	"github.com/Hoosat-Oy/HTND/domain/utxoindex"
)

var sigBuf [256]byte

func fastHex(dst []byte, src []byte) string {
	n := hex.Encode(dst, src)
	return string(dst[:n])
}

// ConvertUTXOOutpointEntryPairToUTXOsByAddressesEntry converts
// a UTXOOutpointEntryPair to a slice of UTXOsByAddressesEntry
func ConvertUTXOOutpointEntryPairToUTXOsByAddressesEntry(address string, pair utxoindex.UTXOPair) *appmessage.UTXOsByAddressesEntry {

	// Compute scriptHex once per address (all UTXOs for this address share the same ScriptPublicKey)
	var scriptHex string
	var scriptVersion uint16
	scriptHex = fastHex(sigBuf[:], pair.Entry.ScriptPublicKey().Script)
	scriptVersion = pair.Entry.ScriptPublicKey().Version

	sharedScript := &appmessage.RPCScriptPublicKey{
		Script:  scriptHex,
		Version: scriptVersion,
	}

	entry := &appmessage.RPCUTXOEntry{
		Amount:          pair.Entry.Amount(),
		ScriptPublicKey: sharedScript,
		BlockDAAScore:   pair.Entry.BlockDAAScore(),
		IsCoinbase:      pair.Entry.IsCoinbase(),
	}

	return &appmessage.UTXOsByAddressesEntry{
		Address: address,
		Outpoint: &appmessage.RPCOutpoint{
			TransactionID: pair.Outpoint.TransactionID.String(),
			Index:         pair.Outpoint.Index,
		},
		UTXOEntry: entry,
	}
}

// ConvertAddressStringsToUTXOsChangedNotificationAddresses converts address strings
// to UTXOsChangedNotificationAddresses
func (ctx *Context) ConvertAddressStringsToUTXOsChangedNotificationAddresses(
	addressStrings []string) ([]*UTXOsChangedNotificationAddress, error) {

	addresses := make([]*UTXOsChangedNotificationAddress, len(addressStrings))
	for i, addressString := range addressStrings {
		address, err := util.DecodeAddress(addressString, ctx.Config.ActiveNetParams.Prefix)
		if err != nil {
			return nil, errors.Errorf("Could not decode address '%s': %s", addressString, err)
		}
		scriptPublicKey, err := txscript.PayToAddrScript(address)
		if err != nil {
			return nil, errors.Errorf("Could not create a scriptPublicKey for address '%s': %s", addressString, err)
		}
		scriptPublicKeyString := utxoindex.ScriptPublicKeyString(fastHex(sigBuf[:], scriptPublicKey.Script))
		addresses[i] = &UTXOsChangedNotificationAddress{
			Address:               addressString,
			ScriptPublicKeyString: scriptPublicKeyString,
		}
	}
	return addresses, nil
}
