package rpchandlers

import (
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/HoosatNetwork/HTND/v2/app/rpc/rpccontext"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/config"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/os/signal"
)

// TestHandleShutDownTwice pins that repeated ShutDown calls request the shutdown without crashing. The
// handler used to close the interrupt channel, which the signal listener owns and closes itself, so a
// second call - or one after Ctrl+C - panicked with "close of closed channel" and took the process down.
func TestHandleShutDownTwice(t *testing.T) {
	interrupt := signal.InterruptListener()
	context := &rpccontext.Context{Config: config.DefaultConfig(), ShutDownChan: interrupt}

	for range 2 {
		response, err := HandleShutDown(context, nil, appmessage.NewShutDownRequestMessage())
		if err != nil {
			t.Fatalf("HandleShutDown: %+v", err)
		}
		if shutDownResponse := response.(*appmessage.ShutDownResponseMessage); shutDownResponse.Error != nil {
			t.Fatalf("unexpected RPC error: %s", shutDownResponse.Error.Message)
		}
	}

	select {
	case <-interrupt:
	case <-time.After(pauseBeforeShutDown + 5*time.Second):
		t.Fatalf("ShutDown did not request a shutdown")
	}
	// Let the second call's goroutine finish; a double close would panic and exit the test binary here.
	time.Sleep(pauseBeforeShutDown / 2)
}
