package rpchandlers

import (
	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/HoosatNetwork/HTND/v2/app/rpc/rpccontext"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/router"
)

// HandleResolveFinalityConflict handles the respectively named RPC command
func HandleResolveFinalityConflict(context *rpccontext.Context, _ *router.Router, _ appmessage.Message) (appmessage.Message, error) {
	if context.Config.SafeRPC {
		log.Warn("ResolveFinalityConflict RPC command called while node in safe RPC mode -- ignoring.")
		response := &appmessage.ResolveFinalityConflictResponseMessage{}
		response.Error = appmessage.RPCErrorf("ResolveFinalityConflict RPC command called while node in safe RPC mode")
		return response, nil
	}

	response := &appmessage.ResolveFinalityConflictResponseMessage{}
	response.Error = appmessage.RPCErrorf("not implemented")
	return response, nil
}
