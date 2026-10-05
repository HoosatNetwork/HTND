package rpchandlers

import (
	"time"

	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/HoosatNetwork/HTND/v2/app/rpc/rpccontext"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/router"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/os/signal"
)

const pauseBeforeShutDown = time.Second

// HandleShutDown handles the respectively named RPC command
func HandleShutDown(context *rpccontext.Context, _ *router.Router, _ appmessage.Message) (appmessage.Message, error) {
	if context.Config.SafeRPC {
		log.Warn("ShutDown RPC command called while node in safe RPC mode -- ignoring.")
		response := appmessage.NewShutDownResponseMessage()
		response.Error = appmessage.RPCErrorf("ShutDown RPC command called while node in safe RPC mode")
		return response, nil
	}

	log.Warn("ShutDown RPC called.")

	// Wait a second before shutting down, to allow time to return the response to the caller.
	//
	// The shutdown is requested through the interrupt listener, as any subsystem requests one, rather
	// than by closing context.ShutDownChan. That channel is the listener's own, which it closes itself on
	// SIGINT, so closing it here panicked with "close of closed channel" after Ctrl+C or a second
	// ShutDown call - exiting through the panic handler instead of shutting down. The listener accepts
	// repeated requests.
	spawn("HandleShutDown-pauseAndShutDown", func() {
		<-time.After(pauseBeforeShutDown)
		signal.ShutdownRequestChannel <- struct{}{}
	})

	response := appmessage.NewShutDownResponseMessage()
	return response, nil
}
