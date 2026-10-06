package rpc

import (
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/HoosatNetwork/HTND/v2/app/rpc/rpccontext"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/router"
)

// TestAddressIndexRequestDoesNotHoldUpOtherRequestsFromTheSameClient pins that a slow address-index request does not
// hold up a client's other requests. A client's requests used to be handled one at a time, and since the address-index
// RPCs check every coin of an address against virtual's UTXO set, a balance query for a mining pool's address ran for
// minutes on htnd5 on 2026-09-15 while the same client's GetBlockTemplate and SubmitBlock requests waited behind it, and
// mining stopped.
func TestAddressIndexRequestDoesNotHoldUpOtherRequestsFromTheSameClient(t *testing.T) {
	release := make(chan struct{})
	originalBalancesHandler := handlers[appmessage.CmdGetBalancesByAddressesRequestMessage]
	originalInfoHandler := handlers[appmessage.CmdGetInfoRequestMessage]
	handlers[appmessage.CmdGetBalancesByAddressesRequestMessage] = func(*rpccontext.Context, *router.Router,
		appmessage.Message,
	) (appmessage.Message, error) {
		<-release
		return appmessage.NewGetBalancesByAddressesResponse(nil), nil
	}
	handlers[appmessage.CmdGetInfoRequestMessage] = func(*rpccontext.Context, *router.Router,
		appmessage.Message,
	) (appmessage.Message, error) {
		return &appmessage.GetInfoResponseMessage{}, nil
	}

	manager := &Manager{context: &rpccontext.Context{}}
	rtr := router.NewRouter("TestAddressIndexRequestDoesNotHoldUpOtherRequestsFromTheSameClient")
	incomingRoute, err := rtr.AddIncomingRoute("rpc router", []appmessage.MessageCommand{
		appmessage.CmdGetBalancesByAddressesRequestMessage, appmessage.CmdGetInfoRequestMessage,
	})
	if err != nil {
		t.Fatalf("AddIncomingRoute: %+v", err)
	}
	done := make(chan error, 1)
	go func() { done <- manager.handleIncomingMessages(rtr, incomingRoute, "test client", nil) }()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
		rtr.Close()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Errorf("handleIncomingMessages did not return after the router closed")
		}
		handlers[appmessage.CmdGetBalancesByAddressesRequestMessage] = originalBalancesHandler
		handlers[appmessage.CmdGetInfoRequestMessage] = originalInfoHandler
	}()

	if err := incomingRoute.Enqueue(appmessage.NewGetBalancesByAddressesRequest([]string{"an address with many coins"})); err != nil {
		t.Fatalf("Enqueue: %+v", err)
	}
	if err := incomingRoute.Enqueue(appmessage.NewGetInfoRequestMessage()); err != nil {
		t.Fatalf("Enqueue: %+v", err)
	}

	response, err := rtr.OutgoingRoute().DequeueWithTimeout(2 * time.Second)
	if err != nil {
		t.Fatalf("GetInfo was not answered while a GetBalancesByAddresses request from the same client was still "+
			"being handled: %v", err)
	}
	if response.Command() != appmessage.CmdGetInfoResponseMessage {
		t.Fatalf("expected the GetInfo response first, got %s", response.Command())
	}

	close(release)
	response, err = rtr.OutgoingRoute().DequeueWithTimeout(2 * time.Second)
	if err != nil {
		t.Fatalf("the GetBalancesByAddresses request was not answered after its handler finished: %v", err)
	}
	if response.Command() != appmessage.CmdGetBalancesByAddressesResponseMessage {
		t.Fatalf("expected the GetBalancesByAddresses response, got %s", response.Command())
	}
}
