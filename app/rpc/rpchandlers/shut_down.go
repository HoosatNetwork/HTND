package rpchandlers

import (
	"time"

	"github.com/Hoosat-Oy/HTND/app/appmessage"
	"github.com/Hoosat-Oy/HTND/app/rpc/rpccontext"
	"github.com/Hoosat-Oy/HTND/infrastructure/network/netadapter/router"
)

const pauseBeforeShutDown = time.Second

// HandleShutDown handles the respectively named RPC command
func HandleShutDown(context *rpccontext.Context, _ *router.Router, _ appmessage.Message) (appmessage.Message, error) {
	isNearlySynced, err := context.Domain.Consensus().IsNearlySynced()
	if err != nil {
		return nil, err
	}
	if !isNearlySynced && context.Config.ActiveNetParams.Net == appmessage.Mainnet {
		return appmessage.NewShutDownResponseMessage(), nil
	}
	if context.Config.SafeRPC {
		log.Warn("ShutDown RPC command called while node in safe RPC mode -- ignoring.")
		response := appmessage.NewShutDownResponseMessage()
		response.Error =
			appmessage.RPCErrorf("ShutDown RPC command called while node in safe RPC mode")
		return response, nil
	}

	log.Warn("ShutDown RPC called.")

	// Wait a second before shutting down, to allow time to return the response to the caller
	spawn("HandleShutDown-pauseAndShutDown", func() {
		<-time.After(pauseBeforeShutDown)
		close(context.ShutDownChan)
	})

	response := appmessage.NewShutDownResponseMessage()
	return response, nil
}
