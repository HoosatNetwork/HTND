package rpccontext

import (
	"encoding/hex"

	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/txscript"
	"github.com/Hoosat-Oy/HTND/util"
	"github.com/pkg/errors"

	"github.com/Hoosat-Oy/HTND/app/appmessage"
	"github.com/Hoosat-Oy/HTND/domain/utxoindex"
)

// ConvertUTXOOutpointEntryPairsToUTXOsByAddressesEntries converts
// UTXOOutpointEntryPairs to a slice of UTXOsByAddressesEntry
func ConvertUTXOOutpointEntryPairsToUTXOsByAddressesEntries(address string, pairs utxoindex.UTXOOutpointEntryPairs) []*appmessage.UTXOsByAddressesEntry {
	utxosByAddressesEntries := make([]*appmessage.UTXOsByAddressesEntry, 0, len(pairs))

	// Compute scriptHex once per address (all UTXOs for this address share the same ScriptPublicKey)
	var scriptHex string
	var scriptVersion uint16
	for _, utxoEntry := range pairs {
		scriptHex = hex.EncodeToString(utxoEntry.ScriptPublicKey().Script)
		scriptVersion = utxoEntry.ScriptPublicKey().Version
		break
	}

	for outpoint, utxoEntry := range pairs {
		// IMPORTANT: do NOT use sync.Pool for objects that you store in the returned slice.
		// Each returned entry must have its own RPCUTXOEntry instance.
		entry := &appmessage.RPCUTXOEntry{
			Amount: utxoEntry.Amount(),
			ScriptPublicKey: &appmessage.RPCScriptPublicKey{
				Script:  scriptHex,
				Version: scriptVersion,
			},
			BlockDAAScore: utxoEntry.BlockDAAScore(),
			IsCoinbase:    utxoEntry.IsCoinbase(),
		}

		utxosByAddressesEntries = append(utxosByAddressesEntries, &appmessage.UTXOsByAddressesEntry{
			Address: address,
			Outpoint: &appmessage.RPCOutpoint{
				TransactionID: outpoint.TransactionID.String(),
				Index:         outpoint.Index,
			},
			UTXOEntry: entry,
		})
	}

	return utxosByAddressesEntries
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
		scriptPublicKeyString := utxoindex.ScriptPublicKeyString(scriptPublicKey.String())
		addresses[i] = &UTXOsChangedNotificationAddress{
			Address:               addressString,
			ScriptPublicKeyString: scriptPublicKeyString,
		}
	}
	return addresses, nil
}
