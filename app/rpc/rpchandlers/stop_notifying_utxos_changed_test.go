package rpchandlers

import (
	"testing"

	"github.com/HoosatNetwork/HTND/app/appmessage"
	"github.com/HoosatNetwork/HTND/app/rpc/rpccontext"
	"github.com/HoosatNetwork/HTND/infrastructure/config"
)

// TestHandleStopNotifyingUTXOsChangedParseErrorResponseType pins that an unparseable address is answered
// with a StopNotifyingUTXOsChanged response. It used to be answered with a NotifyUTXOsChanged response,
// which the caller never receives on the route it waits on, and which the next NotifyUTXOsChanged call
// then reads as its own reply.
func TestHandleStopNotifyingUTXOsChangedParseErrorResponseType(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.UTXOIndex = true
	context := &rpccontext.Context{Config: cfg}

	request := appmessage.NewStopNotifyingUTXOsChangedRequestMessage([]string{"not an address"})
	response, err := HandleStopNotifyingUTXOsChanged(context, nil, request)
	if err != nil {
		t.Fatalf("HandleStopNotifyingUTXOsChanged: %+v", err)
	}
	stopResponse, ok := response.(*appmessage.StopNotifyingUTXOsChangedResponseMessage)
	if !ok {
		t.Fatalf("expected a %s, got a %s", appmessage.CmdStopNotifyingUTXOsChangedResponseMessage, response.Command())
	}
	if stopResponse.Error == nil {
		t.Fatalf("expected a parsing error for an invalid address")
	}
}
