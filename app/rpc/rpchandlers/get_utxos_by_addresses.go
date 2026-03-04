package rpchandlers

import (
	"encoding/hex"
	"sync"

	"github.com/Hoosat-Oy/HTND/app/appmessage"
	"github.com/Hoosat-Oy/HTND/app/rpc/rpccontext"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/txscript"
	"github.com/Hoosat-Oy/HTND/infrastructure/network/netadapter/router"
	"github.com/Hoosat-Oy/HTND/util"
)

var sigBuf [256]byte

var utxoEntriesPool = sync.Pool{
	New: func() interface{} {
		return make([]*appmessage.UTXOsByAddressesEntry, 0, 1000)
	},
}

func fastHex(dst []byte, src []byte) string {
	n := hex.Encode(dst, src)
	return string(dst[:n])
}

// HandleGetUTXOsByAddresses handles the respectively named RPC command with 1-second cache
func HandleGetUTXOsByAddresses(context *rpccontext.Context, _ *router.Router, request appmessage.Message) (appmessage.Message, error) {
	if !context.Config.UTXOIndex {
		errorMessage := &appmessage.GetUTXOsByAddressesResponseMessage{}
		errorMessage.Error = appmessage.RPCErrorf("Method unavailable when htnd is run without --utxoindex")
		return errorMessage, nil
	}

	getUTXOsByAddressesRequest := request.(*appmessage.GetUTXOsByAddressesRequestMessage)

	if getUTXOsByAddressesRequest.Limit == 0 || (getUTXOsByAddressesRequest.Limit > context.Config.UTXODefaultMaxLimit && context.Config.UTXODefaultMaxLimit != 0) {
		getUTXOsByAddressesRequest.Limit = context.Config.UTXODefaultMaxLimit
	}

	// Set a reasonable total limit to prevent excessive memory allocation
	// Preallocate with initial capacity to reduce reallocations
	allEntries := utxoEntriesPool.Get().([]*appmessage.UTXOsByAddressesEntry)[:0] // Reset length
	needed := int(getUTXOsByAddressesRequest.Limit) * len(getUTXOsByAddressesRequest.Addresses)
	if cap(allEntries) < needed {
		allEntries = make([]*appmessage.UTXOsByAddressesEntry, 0, needed)
	}

	for _, addressString := range getUTXOsByAddressesRequest.Addresses {
		address, err := util.DecodeAddress(addressString, context.Config.ActiveNetParams.Prefix)
		if err != nil {
			errorMessage := &appmessage.GetUTXOsByAddressesResponseMessage{}
			errorMessage.Error = appmessage.RPCErrorf("Could not decode address '%s': %s", addressString, err)
			return errorMessage, nil
		}
		scriptPublicKey, err := txscript.PayToAddrScript(address)
		if err != nil {
			errorMessage := &appmessage.GetUTXOsByAddressesResponseMessage{}
			errorMessage.Error = appmessage.RPCErrorf("Could not create a scriptPublicKey for address '%s': %s", addressString, err)
			return errorMessage, nil
		}
		utxoOutpointEntryPairs, err := context.UTXOIndex.UTXOs(scriptPublicKey, getUTXOsByAddressesRequest.Limit)
		if err != nil {
			return nil, err
		}
		if len(utxoOutpointEntryPairs) == 0 {
			continue
		}
		sharedScript := &appmessage.RPCScriptPublicKey{
			Script:  fastHex(sigBuf[:], utxoOutpointEntryPairs[0].Entry.ScriptPublicKey().Script),
			Version: utxoOutpointEntryPairs[0].Entry.ScriptPublicKey().Version,
		}
		for _, pair := range utxoOutpointEntryPairs {
			allEntries = append(allEntries, rpccontext.ConvertUTXOOutpointEntryPairToUTXOsByAddressesEntry(addressString, sharedScript, pair))
		}
	}

	response := appmessage.NewGetUTXOsByAddressesResponseMessage(allEntries)
	utxoEntriesPool.Put(allEntries) // Return slice to pool for reuse
	return response, nil
}
