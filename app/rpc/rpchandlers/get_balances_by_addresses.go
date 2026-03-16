package rpchandlers

import (
	"strings"
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

const balancesByAddressesCacheTTL = time.Second

var balancesByAddressesPool = sync.Pool{
	New: func() interface{} {
		return make([]*appmessage.BalancesByAddressesEntry, 0, 2)
	},
}

func releaseBalancesByAddressesEntries(entries []*appmessage.BalancesByAddressesEntry) {
	clear(entries[:cap(entries)])
	balancesByAddressesPool.Put(entries[:0])
}

func purgeExpiredBalancesByAddressesCache(now time.Time) {
	for key, entry := range balancesByAddressesCache {
		if now.Sub(entry.timestamp) >= balancesByAddressesCacheTTL {
			delete(balancesByAddressesCache, key)
		}
	}
}

// HandleGetBalancesByAddresses handles the respectively named RPC command
func HandleGetBalancesByAddresses(context *rpccontext.Context, _ *router.Router, request appmessage.Message) (appmessage.Message, error) {

	if !context.Config.UTXOIndex {
		errorMessage := &appmessage.GetBalancesByAddressesResponseMessage{}
		errorMessage.Error = appmessage.RPCErrorf("Method unavailable when htnd is run without --utxoindex")
		return errorMessage, nil
	}

	getBalancesByAddressesRequest := request.(*appmessage.GetBalancesByAddressesRequestMessage)

	totalAddressLength := 0
	for _, addr := range getBalancesByAddressesRequest.Addresses {
		totalAddressLength += len(addr) + 1
	}
	var cacheKeyBuilder strings.Builder
	cacheKeyBuilder.Grow(totalAddressLength)
	for _, addr := range getBalancesByAddressesRequest.Addresses {
		cacheKeyBuilder.WriteString(addr)
		cacheKeyBuilder.WriteByte(',')
	}
	cacheKey := cacheKeyBuilder.String()

	balancesByAddressesCacheMutex.Lock()
	now := time.Now()
	purgeExpiredBalancesByAddressesCache(now)
	cached, found := balancesByAddressesCache[cacheKey]
	if found {
		balancesByAddressesCacheMutex.Unlock()
		response := appmessage.NewGetBalancesByAddressesResponse(cached.balances)
		return response, nil
	}
	balancesByAddressesCacheMutex.Unlock()

	allEntries := balancesByAddressesPool.Get().([]*appmessage.BalancesByAddressesEntry)[:0]
	if cap(allEntries) < len(getBalancesByAddressesRequest.Addresses) {
		allEntries = make([]*appmessage.BalancesByAddressesEntry, 0, len(getBalancesByAddressesRequest.Addresses))
	}
	defer releaseBalancesByAddressesEntries(allEntries)
	allEntries = allEntries[:len(getBalancesByAddressesRequest.Addresses)]
	for i, address := range getBalancesByAddressesRequest.Addresses {
		balance, err := getBalanceByAddress(context, address, 0)

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
	responseEntries := make([]*appmessage.BalancesByAddressesEntry, len(allEntries))
	copy(responseEntries, allEntries)
	balancesByAddressesCacheMutex.Lock()
	purgeExpiredBalancesByAddressesCache(now)
	balancesByAddressesCache[cacheKey] = struct {
		balances  []*appmessage.BalancesByAddressesEntry
		timestamp time.Time
	}{
		balances:  responseEntries,
		timestamp: now,
	}
	balancesByAddressesCacheMutex.Unlock()
	response := appmessage.NewGetBalancesByAddressesResponse(responseEntries)
	return response, nil
}
