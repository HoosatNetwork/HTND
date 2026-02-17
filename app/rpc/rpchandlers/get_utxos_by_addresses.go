package rpchandlers

import (
	"sync"
	"time"

	"github.com/Hoosat-Oy/HTND/app/appmessage"
	"github.com/Hoosat-Oy/HTND/app/rpc/rpccontext"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/txscript"
	"github.com/Hoosat-Oy/HTND/domain/utxoindex"
	"github.com/Hoosat-Oy/HTND/infrastructure/network/netadapter/router"
	"github.com/Hoosat-Oy/HTND/util"
)

var (
	utxosByAddressesCache = make(map[string]struct {
		entries   []*appmessage.UTXOsByAddressesEntry
		timestamp time.Time
	})
	utxosByAddressesCacheMutex sync.Mutex
)

// HandleGetUTXOsByAddresses handles the respectively named RPC command with 1-second cache
func HandleGetUTXOsByAddresses(context *rpccontext.Context, _ *router.Router, request appmessage.Message) (appmessage.Message, error) {
	if !context.Config.UTXOIndex {
		errorMessage := &appmessage.GetUTXOsByAddressesResponseMessage{}
		errorMessage.Error = appmessage.RPCErrorf("Method unavailable when htnd is run without --utxoindex")
		return errorMessage, nil
	}

	getUTXOsByAddressesRequest := request.(*appmessage.GetUTXOsByAddressesRequestMessage)

	// Create a cache key based on addresses
	cacheKey := ""
	for _, addr := range getUTXOsByAddressesRequest.Addresses {
		cacheKey += addr + ","
	}

	utxosByAddressesCacheMutex.Lock()
	cached, found := utxosByAddressesCache[cacheKey]
	if found && time.Since(cached.timestamp) < time.Second {
		utxosByAddressesCacheMutex.Unlock()
		response := appmessage.NewGetUTXOsByAddressesResponseMessage(cached.entries)
		return response, nil
	}
	utxosByAddressesCacheMutex.Unlock()

	total := 0
	utxoPairsByAddress := make([][]utxoindex.UTXOPair, len(getUTXOsByAddressesRequest.Addresses))
	for i, addressString := range getUTXOsByAddressesRequest.Addresses {
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
		utxoOutpointEntryPairs, err := context.UTXOIndex.UTXOs(scriptPublicKey)
		if err != nil {
			return nil, err
		}
		utxoPairsByAddress[i] = utxoOutpointEntryPairs
		total += len(utxoOutpointEntryPairs)
	}

	allEntries := make([]*appmessage.UTXOsByAddressesEntry, 0, total)
	for i, addressString := range getUTXOsByAddressesRequest.Addresses {
		entries := rpccontext.ConvertUTXOOutpointEntryPairsToUTXOsByAddressesEntries(addressString, utxoPairsByAddress[i])
		allEntries = append(allEntries, entries...)
	}

	utxosByAddressesCacheMutex.Lock()
	utxosByAddressesCache[cacheKey] = struct {
		entries   []*appmessage.UTXOsByAddressesEntry
		timestamp time.Time
	}{
		entries:   allEntries,
		timestamp: time.Now(),
	}
	utxosByAddressesCacheMutex.Unlock()

	response := appmessage.NewGetUTXOsByAddressesResponseMessage(allEntries)
	return response, nil
}
