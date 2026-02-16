package rpccontext

import (
	"encoding/hex"
	"sync"

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
	var scriptHex string
	var scriptVersion uint16
	var scriptBytes []byte
	// Pool for UTXOEntry objects
	var utxoEntryPool = sync.Pool{
		New: func() interface{} { return new(appmessage.RPCUTXOEntry) },
	}
	// Compute scriptHex and scriptBytes once per address
	for _, utxoEntry := range pairs {
		scriptBytes = utxoEntry.ScriptPublicKey().Script
		scriptHex = hex.EncodeToString(scriptBytes)
		scriptVersion = utxoEntry.ScriptPublicKey().Version
		break // Only need to compute once
	}
	for outpoint, utxoEntry := range pairs {
		pooledEntry := utxoEntryPool.Get().(*appmessage.RPCUTXOEntry)
		// Reset fields (if needed)
		pooledEntry.Amount = utxoEntry.Amount()
		pooledEntry.ScriptPublicKey = &appmessage.RPCScriptPublicKey{
			Script:  scriptHex,
			Version: scriptVersion,
		}
		pooledEntry.BlockDAAScore = utxoEntry.BlockDAAScore()
		pooledEntry.IsCoinbase = utxoEntry.IsCoinbase()
		utxosByAddressesEntries = append(utxosByAddressesEntries, &appmessage.UTXOsByAddressesEntry{
			Address: address,
			Outpoint: &appmessage.RPCOutpoint{
				TransactionID: outpoint.TransactionID.String(),
				Index:         outpoint.Index,
			},
			UTXOEntry: pooledEntry,
		})
		utxoEntryPool.Put(pooledEntry)
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
