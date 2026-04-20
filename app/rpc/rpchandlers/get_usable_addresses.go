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
	"github.com/pkg/errors"
)

var (
	usableAddressesCache      = make(map[string]usableAddressCacheEntry)
	usableAddressesCacheMutex sync.Mutex
)

const (
	// usableAddressesCacheTTL trades slight staleness for vastly reduced disk IO when
	// clients repeatedly query the same derived addresses (e.g. wallet sync loop).
	usableAddressesCacheTTL = 30 * time.Second
	// usableAddressesCacheMaxEntries is a safety bound to avoid unbounded memory growth
	// in case clients continuously query unique addresses.
	usableAddressesCacheMaxEntries = 200_000
)

type usableAddressCacheEntry struct {
	usable    bool
	checkedAt time.Time
}

func getUsabilityOfAddress(context *rpccontext.Context, addressString string) (bool, error) {
	address, err := util.DecodeAddress(addressString, context.Config.ActiveNetParams.Prefix)
	if err != nil {
		return false, appmessage.RPCErrorf("Couldn't decode address '%s': %s", addressString, err)
	}

	scriptPublicKey, err := txscript.PayToAddrScript(address)
	if err != nil {
		return false, appmessage.RPCErrorf("Could not create a scriptPublicKey for address '%s': %s", addressString, err)
	}
	// Cache the result for a short time to avoid repeated DB lookups.
	// NOTE: We intentionally do not cache syncing errors (ErrUTXOIndexSyncing).
	now := time.Now()
	usableAddressesCacheMutex.Lock()
	if entry, ok := usableAddressesCache[addressString]; ok {
		if now.Sub(entry.checkedAt) <= usableAddressesCacheTTL {
			usableAddressesCacheMutex.Unlock()
			return entry.usable, nil
		}
	}
	usableAddressesCacheMutex.Unlock()

	hasUTXOs, err := context.UTXOIndex.HasUTXOs(scriptPublicKey)
	if err != nil {
		if errors.Is(err, utxoindex.ErrUTXOIndexSyncing) {
			return false, appmessage.RPCErrorf("UTXO index is resyncing after a pruning-point update; retry shortly")
		}
		return false, err
	}

	usableAddressesCacheMutex.Lock()
	// Simple safety bound: if the cache grows too big (e.g. scanning huge ranges), clear it.
	if len(usableAddressesCache) >= usableAddressesCacheMaxEntries {
		usableAddressesCache = make(map[string]usableAddressCacheEntry)
	}
	usableAddressesCache[addressString] = usableAddressCacheEntry{usable: hasUTXOs, checkedAt: now}
	usableAddressesCacheMutex.Unlock()

	return hasUTXOs, nil
}

var usableAddressesPool = sync.Pool{
	New: func() interface{} {
		slice := make([]string, 0, 2)
		return &slice
	},
}

func releaseUsableAddresses(addresses []string) {
	clear(addresses[:cap(addresses)])
	addresses = addresses[:0]
	usableAddressesPool.Put(&addresses)
}

// HandleGetUsableAddresses handles the respectively named RPC command
func HandleGetUsableAddresses(context *rpccontext.Context, _ *router.Router, request appmessage.Message) (appmessage.Message, error) {
	if !context.Config.UTXOIndex {
		errorMessage := &appmessage.GetUsableAddressesResponseMessage{}
		errorMessage.Error = appmessage.RPCErrorf("Method unavailable when htnd is run without --utxoindex")
		return errorMessage, nil
	}

	// log.Infof("-----------------------------------------------------------")
	// log.Infof("Handling GetUsableAddressesRequest")
	// log.Infof("-----------------------------------------------------------")

	getUsableAddressesRequest := request.(*appmessage.GetUsableAddressesRequestMessage)

	UsableAddresses := (*usableAddressesPool.Get().(*[]string))[:0]
	if cap(UsableAddresses) < len(getUsableAddressesRequest.Addresses) {
		UsableAddresses = make([]string, 0, len(getUsableAddressesRequest.Addresses))
	}
	defer releaseUsableAddresses(UsableAddresses)
	for _, address := range getUsableAddressesRequest.Addresses {
		usable, err := getUsabilityOfAddress(context, address)
		if err != nil {
			rpcError := &appmessage.RPCError{}
			if !errors.As(err, &rpcError) {
				return nil, err
			}
			errorMessage := &appmessage.GetUsableAddressesResponseMessage{}
			errorMessage.Error = rpcError
			return errorMessage, nil
		}
		if usable {
			UsableAddresses = append(UsableAddresses, address)
		}
	}
	// log.Infof("-----------------------------------------------------------")
	// log.Infof("Found %s usable addresses", len(UsableAddresses))
	// log.Infof("-----------------------------------------------------------")
	responseAddresses := append(make([]string, 0, len(UsableAddresses)), UsableAddresses...)
	response := appmessage.NewGetUsableAddressesResponse(responseAddresses)
	return response, nil
}
