package rpchandlers

import (
	"encoding/hex"

	"github.com/Hoosat-Oy/HTND/app/appmessage"
	"github.com/Hoosat-Oy/HTND/app/rpc/rpccontext"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/txscript"
	"github.com/Hoosat-Oy/HTND/domain/utxoindex"
	"github.com/Hoosat-Oy/HTND/infrastructure/network/netadapter/router"
	"github.com/Hoosat-Oy/HTND/util"
	"github.com/Hoosat-Oy/HTND/util/memory"
)

func encodeHexString(buffer []byte, value []byte) ([]byte, string) {
	if value == nil {
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

// HandleGetUTXOsByAddresses handles the respectively named RPC command with 1-second cache
func HandleGetUTXOsByAddresses(context *rpccontext.Context, _ *router.Router, request appmessage.Message) (appmessage.Message, error) {
	if !context.Config.UTXOIndex {
		errorMessage := &appmessage.GetUTXOsByAddressesResponseMessage{}
		errorMessage.Error = appmessage.RPCErrorf("Method unavailable when htnd is run without --utxoindex")
		return errorMessage, nil
	}

	getUTXOsByAddressesRequest := request.(*appmessage.GetUTXOsByAddressesRequestMessage)

	if getUTXOsByAddressesRequest.Limit < 0 || getUTXOsByAddressesRequest.Limit > 10_000_000 {
		errorMessage := &appmessage.GetUTXOsByAddressesResponseMessage{}
		errorMessage.Error = appmessage.RPCErrorf("Invalid limit: %d. Limit must be between 0 and 10,000,000", getUTXOsByAddressesRequest.Limit)
		return errorMessage, nil
	}

	if getUTXOsByAddressesRequest.Limit == 0 || (getUTXOsByAddressesRequest.Limit > context.Config.UTXODefaultMaxLimit && context.Config.UTXODefaultMaxLimit != 0) {
		getUTXOsByAddressesRequest.Limit = context.Config.UTXODefaultMaxLimit
	}

	allEntries := make([]*appmessage.UTXOsByAddressesEntry, 0, len(getUTXOsByAddressesRequest.Addresses))

	var reusableHexBuffer []byte

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

		utxoOutpointEntryPairsBuffer := memory.Malloc[utxoindex.UTXOPair](1000)
		if utxoOutpointEntryPairsBuffer == nil {
			errorMessage := &appmessage.GetUTXOsByAddressesResponseMessage{}
			errorMessage.Error = appmessage.RPCErrorf("Could not allocate memory for address '%s'", addressString)
			return errorMessage, nil
		}
		utxoOutpointEntryPairs, err := context.UTXOIndex.UTXOs(scriptPublicKey, getUTXOsByAddressesRequest.Limit, utxoOutpointEntryPairsBuffer)
		if err != nil {
			memory.Free(utxoOutpointEntryPairsBuffer)
			return nil, err
		}
		if len(utxoOutpointEntryPairs) == 0 {
			memory.Free(utxoOutpointEntryPairsBuffer)
			continue
		}
		var script []byte
		for _, pair := range utxoOutpointEntryPairs {
			if pair.Entry.ScriptPublicKey().Script != nil {
				script = pair.Entry.ScriptPublicKey().Script
				break
			}
		}
		var scriptHex string
		reusableHexBuffer, scriptHex = encodeHexString(reusableHexBuffer, script)
		sharedScript := &appmessage.RPCScriptPublicKey{
			Script:  scriptHex,
			Version: utxoOutpointEntryPairs[0].Entry.ScriptPublicKey().Version,
		}
		for _, pair := range utxoOutpointEntryPairs {
			allEntries = append(allEntries, rpccontext.ConvertUTXOOutpointEntryPairToUTXOsByAddressesEntry(addressString, sharedScript, pair))
		}
		memory.Free(utxoOutpointEntryPairsBuffer)
	}

	response := appmessage.NewGetUTXOsByAddressesResponseMessage(allEntries)
	return response, nil
}
