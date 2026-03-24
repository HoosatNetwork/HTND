package rpchandlers

import (
	"github.com/Hoosat-Oy/HTND/app/appmessage"
	"github.com/Hoosat-Oy/HTND/app/rpc/rpccontext"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/txscript"
	"github.com/Hoosat-Oy/HTND/domain/utxoindex"
	"github.com/Hoosat-Oy/HTND/infrastructure/network/netadapter/router"
	"github.com/Hoosat-Oy/HTND/util"
	"github.com/Hoosat-Oy/HTND/util/memory"
)

// HandleGetPaginatedUTXOsByAddresses handles the respectively named RPC command with 1-second cache
func HandleGetPaginatedUTXOsByAddresses(context *rpccontext.Context, _ *router.Router, request appmessage.Message) (appmessage.Message, error) {
	if !context.Config.UTXOIndex {
		errorMessage := &appmessage.GetPaginatedUTXOsByAddressesResponseMessage{}
		errorMessage.Error = appmessage.RPCErrorf("Method unavailable when htnd is run without --utxoindex")
		return errorMessage, nil
	}

	getPaginatedUTXOsByAddressesRequest := request.(*appmessage.GetPaginatedUTXOsByAddressesRequestMessage)

	if getPaginatedUTXOsByAddressesRequest.Limit < 0 || getPaginatedUTXOsByAddressesRequest.Limit > 10_000_000 {
		errorMessage := &appmessage.GetPaginatedUTXOsByAddressesResponseMessage{}
		errorMessage.Error = appmessage.RPCErrorf("Invalid limit: %d. Limit must be between 0 and 10,000,000", getPaginatedUTXOsByAddressesRequest.Limit)
		return errorMessage, nil
	}

	if getPaginatedUTXOsByAddressesRequest.Limit == 0 || (getPaginatedUTXOsByAddressesRequest.Limit > context.Config.UTXODefaultMaxLimit && context.Config.UTXODefaultMaxLimit != 0) {
		getPaginatedUTXOsByAddressesRequest.Limit = context.Config.UTXODefaultMaxLimit
	}
	allEntries := make([]*appmessage.UTXOsByAddressesEntry, 0, len(getPaginatedUTXOsByAddressesRequest.Addresses))

	var reusableHexBuffer []byte
	for _, addressString := range getPaginatedUTXOsByAddressesRequest.Addresses {
		address, err := util.DecodeAddress(addressString, context.Config.ActiveNetParams.Prefix)
		if err != nil {
			errorMessage := &appmessage.GetPaginatedUTXOsByAddressesResponseMessage{}
			errorMessage.Error = appmessage.RPCErrorf("Could not decode address '%s': %s", addressString, err)
			return errorMessage, nil
		}
		scriptPublicKey, err := txscript.PayToAddrScript(address)
		if err != nil {
			errorMessage := &appmessage.GetPaginatedUTXOsByAddressesResponseMessage{}
			errorMessage.Error = appmessage.RPCErrorf("Could not create a scriptPublicKey for address '%s': %s", addressString, err)
			return errorMessage, nil
		}
		utxoOutpointEntryPairsBuffer := memory.Malloc[utxoindex.UTXOPair](1000)
		if utxoOutpointEntryPairsBuffer == nil {
			errorMessage := &appmessage.GetPaginatedUTXOsByAddressesResponseMessage{}
			errorMessage.Error = appmessage.RPCErrorf("Could not allocate memory for address '%s'", addressString)
			return errorMessage, nil
		}
		utxoOutpointEntryPairs, err := context.UTXOIndex.PaginatedUTXOs(scriptPublicKey, getPaginatedUTXOsByAddressesRequest.Offset, getPaginatedUTXOsByAddressesRequest.Limit, utxoOutpointEntryPairsBuffer)
		if err != nil {
			memory.Free(utxoOutpointEntryPairsBuffer)
			return nil, err
		}
		if len(utxoOutpointEntryPairs) == 0 {
			memory.Free(utxoOutpointEntryPairsBuffer)
			continue
		}
		if utxoOutpointEntryPairs[0].Entry.ScriptPublicKey() == nil {
			memory.Free(utxoOutpointEntryPairsBuffer)
			continue
		}
		var scriptHex string
		reusableHexBuffer, scriptHex = encodeHexString(reusableHexBuffer, utxoOutpointEntryPairs[0].Entry.ScriptPublicKey().Script)
		sharedScript := &appmessage.RPCScriptPublicKey{
			Script:  scriptHex,
			Version: utxoOutpointEntryPairs[0].Entry.ScriptPublicKey().Version,
		}
		for _, pair := range utxoOutpointEntryPairs {
			allEntries = append(allEntries, rpccontext.ConvertUTXOOutpointEntryPairToUTXOsByAddressesEntry(addressString, sharedScript, pair))
		}
		memory.Free(utxoOutpointEntryPairsBuffer)
	}

	response := appmessage.NewGetPaginatedUTXOsByAddressesResponseMessage(allEntries)
	return response, nil
}
