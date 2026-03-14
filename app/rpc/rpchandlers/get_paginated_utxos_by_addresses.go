package rpchandlers

import (
	"sync"

	"github.com/Hoosat-Oy/HTND/app/appmessage"
	"github.com/Hoosat-Oy/HTND/app/rpc/rpccontext"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/txscript"
	"github.com/Hoosat-Oy/HTND/domain/utxoindex"
	"github.com/Hoosat-Oy/HTND/infrastructure/network/netadapter/router"
	"github.com/Hoosat-Oy/HTND/util"
)

var paginatedUtxoEntriesPool = sync.Pool{
	New: func() interface{} {
		return make([]*appmessage.UTXOsByAddressesEntry, 0, 1000)
	},
}

var paginatedUtxoPairPool = sync.Pool{
	New: func() interface{} {
		return make([]utxoindex.UTXOPair, 0, 1000)
	},
}

func releasePaginatedUTXOsByAddressesEntries(entries []*appmessage.UTXOsByAddressesEntry) {
	clear(entries[:cap(entries)])
	utxoEntriesPool.Put(entries[:0])
}

func releasePaginatedUTXOPairs(pairs []utxoindex.UTXOPair) {
	clear(pairs[:cap(pairs)])
	utxoPairPool.Put(pairs[:0])
}

// HandleGetPaginatedUTXOsByAddresses handles the respectively named RPC command with 1-second cache
func HandleGetPaginatedUTXOsByAddresses(context *rpccontext.Context, _ *router.Router, request appmessage.Message) (appmessage.Message, error) {
	if !context.Config.UTXOIndex {
		errorMessage := &appmessage.GetPaginatedUTXOsByAddressesResponseMessage{}
		errorMessage.Error = appmessage.RPCErrorf("Method unavailable when htnd is run without --utxoindex")
		return errorMessage, nil
	}

	getPaginatedUTXOsByAddressesRequest := request.(*appmessage.GetPaginatedUTXOsByAddressesRequestMessage)

	if getPaginatedUTXOsByAddressesRequest.Limit == 0 || (getPaginatedUTXOsByAddressesRequest.Limit > context.Config.UTXODefaultMaxLimit && context.Config.UTXODefaultMaxLimit != 0) {
		getPaginatedUTXOsByAddressesRequest.Limit = context.Config.UTXODefaultMaxLimit
	}

	// Set a reasonable total limit to prevent excessive memory allocation
	// Preallocate with initial capacity to reduce reallocations
	allEntries := utxoEntriesPool.Get().([]*appmessage.UTXOsByAddressesEntry)[:0] // Reset length
	needed := int(getPaginatedUTXOsByAddressesRequest.Limit) * len(getPaginatedUTXOsByAddressesRequest.Addresses)
	if cap(allEntries) < needed {
		allEntries = make([]*appmessage.UTXOsByAddressesEntry, 0, needed)
	}
	defer func() {
		releasePaginatedUTXOsByAddressesEntries(allEntries)
	}()

	utxoOutpointEntryPairs := utxoPairPool.Get().([]utxoindex.UTXOPair)[:0] // Reset length
	defer func() {
		releasePaginatedUTXOPairs(utxoOutpointEntryPairs)
	}()
	var reusableHexBuffer []byte

	for _, addressString := range getPaginatedUTXOsByAddressesRequest.Addresses {
		utxoOutpointEntryPairs = utxoOutpointEntryPairs[:0]
		address, err := util.DecodeAddress(addressString, context.Config.ActiveNetParams.Prefix)
		if err != nil {
			errorMessage := &appmessage.GetPaginatedUTXOsByAddressesResponseMessage{}
			errorMessage.Error = appmessage.RPCErrorf("Could not decode address '%s': %s", addressString, err)
			return errorMessage, nil
		}
		scriptPublicKey, err := txscript.PayToAddrScript(address)
		if err != nil {
			errorMessage := &appmessage.GetUTXOsByAddressesResponseMessage{}
			errorMessage.Error = appmessage.RPCErrorf("Could not create a scriptPublicKey for address '%s': %s", addressString, err)
			return errorMessage, nil
		}
		utxoOutpointEntryPairs, err := context.UTXOIndex.PaginatedUTXOs(scriptPublicKey, getPaginatedUTXOsByAddressesRequest.Offset, getPaginatedUTXOsByAddressesRequest.Limit, utxoOutpointEntryPairs)
		if err != nil {
			return nil, err
		}
		if len(utxoOutpointEntryPairs) == 0 {
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
	}

	responseEntries := make([]*appmessage.UTXOsByAddressesEntry, len(allEntries))
	copy(responseEntries, allEntries)
	response := appmessage.NewGetPaginatedUTXOsByAddressesResponseMessage(responseEntries)
	return response, nil
}
