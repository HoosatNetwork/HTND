package rpchandlers

import (
	"sync"
	"time"

	"github.com/HoosatNetwork/HTND/app/appmessage"
	"github.com/HoosatNetwork/HTND/app/rpc/rpccontext"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/txscript"
	"github.com/HoosatNetwork/HTND/domain/utxoindex"
	"github.com/HoosatNetwork/HTND/infrastructure/network/netadapter/router"
	"github.com/HoosatNetwork/HTND/util"
	"github.com/HoosatNetwork/HTND/util/memory"
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
	for key, entry := range balanceByAddressCache {
		if time.Since(entry.timestamp) >= time.Second {
			delete(balanceByAddressCache, key)
		}
	}
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

	// Summed from the coins themselves rather than from the index's own total, because each one has to
	// be checked against consensus first. A balance that counts coins consensus does not hold tells the
	// wallet it can spend what it cannot, and then disagrees with the coins this same node serves it.
	buffer := memory.Malloc[utxoindex.UTXOPair](1000)
	if buffer == nil {
		return 0, appmessage.RPCErrorf("Could not allocate memory for address '%s'", addressString)
	}
	pairs, buffer, err := context.UTXOIndex.UTXOs(scriptPublicKey, 0, buffer)
	if err != nil {
		memory.Free(buffer)
		if errors.Is(err, utxoindex.ErrUTXOIndexSyncing) {
			return 0, appmessage.RPCErrorf("UTXO index is resyncing after a pruning-point update; retry shortly")
		}
		return 0, err
	}
	defer memory.Free(buffer)

	pairs, withheld, err := rpccontext.FilterUTXOPairsAgainstVirtual(context.Domain.Consensus(), pairs)
	if err != nil {
		return 0, err
	}
	if withheld > 0 {
		log.Warnf("Left %d UTXO(s) of address %s out of its balance: the UTXO index lists them but virtual's "+
			"UTXO set does not hold them. The index has drifted from consensus.", withheld, addressString)
	}

	return sumUTXOPairs(pairs), nil
}

// sumUTXOPairs adds up the coins as consensus describes them. FilterUTXOPairsAgainstVirtual has
// already dropped the coins consensus does not hold and replaced the entries of the ones it does, so
// the amounts summed here are consensus's, not the index's.
func sumUTXOPairs(pairs []utxoindex.UTXOPair) uint64 {
	var balance uint64
	for _, pair := range pairs {
		balance += pair.Entry.Amount()
	}
	return balance
}
