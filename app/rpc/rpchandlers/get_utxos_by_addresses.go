package rpchandlers

import (
	"encoding/hex"

	"github.com/Hoosat-Oy/HTND/app/appmessage"
	"github.com/Hoosat-Oy/HTND/app/rpc/rpccontext"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/txscript"
	"github.com/Hoosat-Oy/HTND/domain/utxoindex"
	"github.com/Hoosat-Oy/HTND/infrastructure/network/netadapter/router"
	"github.com/Hoosat-Oy/HTND/util"
)

var sigBuf [256]byte

func fastHex(dst []byte, src []byte) string {
	n := hex.Encode(dst, src)
	return string(dst[:n])
}

// HandleGetUTXOsByAddresses handles the respectively named RPC command with 1-second cache
func HandleGetUTXOsByAddresses(context *rpccontext.Context, _ *router.Router, request appmessage.Message) (appmessage.Message, error) {
	if !context.Config.UTXOIndex {
		errorMessage := &appmessage.GetUTXOsByAddressesResponseMessage{}
		errorMessage.Error = appmessage.RPCErrorf("Method unavailable when htnd is run without --utxoindex")
		return errorMessage, nil
	}

	getUTXOsByAddressesRequest := request.(*appmessage.GetUTXOsByAddressesRequestMessage)

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
	for i, address := range getUTXOsByAddressesRequest.Addresses {
		if len(utxoPairsByAddress[i]) == 0 {
			continue
		}
		sharedScript := &appmessage.RPCScriptPublicKey{
			Script:  fastHex(sigBuf[:], utxoPairsByAddress[i][0].Entry.ScriptPublicKey().Script),
			Version: utxoPairsByAddress[i][0].Entry.ScriptPublicKey().Version,
		}
		for _, pair := range utxoPairsByAddress[i] {
			allEntries = append(allEntries, rpccontext.ConvertUTXOOutpointEntryPairToUTXOsByAddressesEntry(address, sharedScript, pair))
		}
	}

	response := appmessage.NewGetUTXOsByAddressesResponseMessage(allEntries)
	return response, nil
}
