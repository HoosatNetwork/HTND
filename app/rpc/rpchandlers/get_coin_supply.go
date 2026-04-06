package rpchandlers

import (
	"github.com/Hoosat-Oy/HTND/app/appmessage"
	"github.com/Hoosat-Oy/HTND/app/rpc/rpccontext"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/constants"
	"github.com/Hoosat-Oy/HTND/domain/utxoindex"
	"github.com/Hoosat-Oy/HTND/infrastructure/network/netadapter/router"
	"github.com/pkg/errors"
)

// HandleGetCoinSupply handles the respectively named RPC command
func HandleGetCoinSupply(context *rpccontext.Context, _ *router.Router, _ appmessage.Message) (appmessage.Message, error) {
	if !context.Config.UTXOIndex {
		errorMessage := &appmessage.GetCoinSupplyResponseMessage{}
		errorMessage.Error = appmessage.RPCErrorf("Method unavailable when htnd is run without --utxoindex")
		return errorMessage, nil
	}

	circulatingSompiSupply, err := context.UTXOIndex.GetCirculatingSompiSupply()
	if err != nil {
		if errors.Is(err, utxoindex.ErrUTXOIndexSyncing) {
			errorMessage := &appmessage.GetCoinSupplyResponseMessage{}
			errorMessage.Error = appmessage.RPCErrorf("UTXO index is resyncing after a pruning-point update; retry shortly")
			return errorMessage, nil
		}
		return nil, err
	}

	response := appmessage.NewGetCoinSupplyResponseMessage(
		constants.MaxSompi,
		circulatingSompiSupply,
	)

	return response, nil
}
