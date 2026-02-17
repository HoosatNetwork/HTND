package rpchandlers

import (
	"sync"
	"time"

	"github.com/Hoosat-Oy/HTND/app/appmessage"
	"github.com/Hoosat-Oy/HTND/app/rpc/rpccontext"
	"github.com/Hoosat-Oy/HTND/infrastructure/network/netadapter/router"
	"github.com/pkg/errors"
)

var (
	balancesByAddressesCache = make(map[string]struct {
		balances  []*appmessage.BalancesByAddressesEntry
		timestamp time.Time
	})
	balancesByAddressesCacheMutex sync.Mutex
)

// HandleGetBalancesByAddresses handles the respectively named RPC command
func HandleGetBalancesByAddresses(context *rpccontext.Context, _ *router.Router, request appmessage.Message) (appmessage.Message, error) {

	if !context.Config.UTXOIndex {
		errorMessage := &appmessage.GetBalancesByAddressesResponseMessage{}
		errorMessage.Error = appmessage.RPCErrorf("Method unavailable when htnd is run without --utxoindex")
		return errorMessage, nil
	}

	getBalancesByAddressesRequest := request.(*appmessage.GetBalancesByAddressesRequestMessage)

	cacheKey := ""
	for _, addr := range getBalancesByAddressesRequest.Addresses {
		cacheKey += addr + ","
	}

	balancesByAddressesCacheMutex.Lock()
	cached, found := balancesByAddressesCache[cacheKey]
	if found && time.Since(cached.timestamp) < time.Second {
		balancesByAddressesCacheMutex.Unlock()
		response := appmessage.NewGetBalancesByAddressesResponse(cached.balances)
		return response, nil
	}
	balancesByAddressesCacheMutex.Unlock()

	allEntries := make([]*appmessage.BalancesByAddressesEntry, len(getBalancesByAddressesRequest.Addresses))
	for i, address := range getBalancesByAddressesRequest.Addresses {
		balance, err := getBalanceByAddress(context, address)

		if err != nil {
			rpcError := &appmessage.RPCError{}
			if !errors.As(err, &rpcError) {
				return nil, err
			}
			errorMessage := &appmessage.GetUTXOsByAddressesResponseMessage{}
			errorMessage.Error = rpcError
			return errorMessage, nil
		}
		allEntries[i] = &appmessage.BalancesByAddressesEntry{
			Address: address,
			Balance: balance,
		}
	}
	balancesByAddressesCacheMutex.Lock()
	balancesByAddressesCache[cacheKey] = struct {
		balances  []*appmessage.BalancesByAddressesEntry
		timestamp time.Time
	}{
		balances:  allEntries,
		timestamp: time.Now(),
	}
	balancesByAddressesCacheMutex.Unlock()
	response := appmessage.NewGetBalancesByAddressesResponse(allEntries)
	return response, nil
}
