package rpchandlers

import (
	"sync"
	"time"

	"github.com/Hoosat-Oy/HTND/app/appmessage"
	"github.com/Hoosat-Oy/HTND/app/rpc/rpccontext"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/txscript"
	"github.com/Hoosat-Oy/HTND/infrastructure/network/netadapter/router"
	"github.com/Hoosat-Oy/HTND/util"
	"github.com/pkg/errors"
)

var (
	balanceByAddressCache = make(map[string]struct {
		balance   uint64
		timestamp time.Time
	})
	balanceByAddressCacheMutex sync.Mutex
)

// HandleGetBalanceByAddress handles the respectively named RPC command
func HandleGetBalanceByAddress(context *rpccontext.Context, _ *router.Router, request appmessage.Message) (appmessage.Message, error) {
	if !context.Config.UTXOIndex {
		errorMessage := &appmessage.GetBalanceByAddressResponseMessage{}
		errorMessage.Error = appmessage.RPCErrorf("Method unavailable when htnd is run without --utxoindex")
		return errorMessage, nil
	}
	getBalanceByAddressRequest := request.(*appmessage.GetBalanceByAddressRequestMessage)

	cacheKey := getBalanceByAddressRequest.Address

	balanceByAddressCacheMutex.Lock()
	cached, found := balanceByAddressCache[cacheKey]
	if found && time.Since(cached.timestamp) < time.Second {
		balanceByAddressCacheMutex.Unlock()
		response := appmessage.NewGetBalanceByAddressResponse(cached.balance)
		return response, nil
	}
	balanceByAddressCacheMutex.Unlock()

	balance, err := getBalanceByAddress(context, getBalanceByAddressRequest.Address)
	if err != nil {
		rpcError := &appmessage.RPCError{}
		if !errors.As(err, &rpcError) {
			return nil, err
		}
		errorMessage := &appmessage.GetBalanceByAddressResponseMessage{}
		errorMessage.Error = rpcError
		return errorMessage, nil
	}
	balanceByAddressCacheMutex.Lock()
	balanceByAddressCache[cacheKey] = struct {
		balance   uint64
		timestamp time.Time
	}{
		balance:   balance,
		timestamp: time.Now(),
	}
	balanceByAddressCacheMutex.Unlock()
	response := appmessage.NewGetBalanceByAddressResponse(balance)
	return response, nil
}

func getBalanceByAddress(context *rpccontext.Context, addressString string) (uint64, error) {
	address, err := util.DecodeAddress(addressString, context.Config.ActiveNetParams.Prefix)
	if err != nil {
		return 0, appmessage.RPCErrorf("Couldn't decode address '%s': %s", addressString, err)
	}

	scriptPublicKey, err := txscript.PayToAddrScript(address)
	if err != nil {
		return 0, appmessage.RPCErrorf("Could not create a scriptPublicKey for address '%s': %s", addressString, err)
	}
	utxoOutpointEntryPairs, err := context.UTXOIndex.UTXOs(scriptPublicKey)
	if err != nil {
		return 0, err
	}

	balance := uint64(0)
	for _, pair := range utxoOutpointEntryPairs {
		balance += pair.Entry.Amount()
	}
	return balance, nil
}
