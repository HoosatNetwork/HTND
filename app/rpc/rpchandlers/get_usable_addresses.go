package rpchandlers

import (
	"sync"

	"github.com/Hoosat-Oy/HTND/app/appmessage"
	"github.com/Hoosat-Oy/HTND/app/rpc/rpccontext"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/txscript"
	"github.com/Hoosat-Oy/HTND/infrastructure/network/netadapter/router"
	"github.com/Hoosat-Oy/HTND/util"
	"github.com/pkg/errors"
)

var (
	usableAddressesCache      = make(map[string][]string)
	usableAddressesCacheMutex sync.Mutex
)

func getUsabilityOfAddress(context *rpccontext.Context, addressString string) (bool, error) {
	address, err := util.DecodeAddress(addressString, context.Config.ActiveNetParams.Prefix)
	if err != nil {
		return false, appmessage.RPCErrorf("Couldn't decode address '%s': %s", addressString, err)
	}

	scriptPublicKey, err := txscript.PayToAddrScript(address)
	if err != nil {
		return false, appmessage.RPCErrorf("Could not create a scriptPublicKey for address '%s': %s", addressString, err)
	}
	hasUTXOs, err := context.UTXOIndex.HasUTXOs(scriptPublicKey)
	if err != nil {
		return false, err
	}

	return hasUTXOs, nil
}

var usableAddressesPool = sync.Pool{
	New: func() interface{} {
		return make([]string, 0, 2)
	},
}

func releaseUsableAddresses(addresses []string) {
	clear(addresses[:cap(addresses)])
	usableAddressesPool.Put(addresses[:0])
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

	UsableAddresses := usableAddressesPool.Get().([]string)[:0]
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
			errorMessage := &appmessage.GetUTXOsByAddressesResponseMessage{}
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
