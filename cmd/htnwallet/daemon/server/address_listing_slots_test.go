package server

import (
	"context"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/daemon/pb"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/router"
	netserver "github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/server"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/server/grpcserver"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/server/grpcserver/protowire"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/rpcclient"
	"github.com/HoosatNetwork/HTND/v2/version"
)

func TestShowAddressesWithOccupiedNodeSlots(t *testing.T) {
	const activation = 258_000_000
	admission, err := grpcserver.NewRPCServer(nil, 2)
	if err != nil {
		t.Fatalf("NewRPCServer: %v", err)
	}
	admission.SetOnConnectedHandler(func(connection netserver.Connection) error {
		routes := router.NewRouter("test node")
		incoming, err := routes.AddIncomingRoute("test node requests", []appmessage.MessageCommand{
			appmessage.CmdGetInfoRequestMessage, appmessage.CmdGetBlockDAGInfoRequestMessage,
		})
		if err != nil {
			return err
		}
		connection.SetOnDisconnectedHandler(routes.Close)
		connection.Start(routes)
		go func() {
			for {
				request, err := incoming.Dequeue()
				if err != nil {
					return
				}
				var response appmessage.Message
				switch request.(type) {
				case *appmessage.GetInfoRequestMessage:
					response = appmessage.NewGetInfoResponseMessage("", 0, version.Version(), false, true, true, 0, 0, "")
				case *appmessage.GetBlockDAGInfoRequestMessage:
					response = &appmessage.GetBlockDAGInfoResponseMessage{VirtualDAAScore: activation}
				}
				if err := routes.OutgoingRoute().Enqueue(response); err != nil {
					return
				}
			}
		}()
		return nil
	})

	address := serveAddressTestNode(t, admission.(protowire.RPCServer))
	s := newMLWallet(t, 1, 1).s
	setAddressListingState(t, s, 1)
	foregroundClient := connectAddressTestNode(t, address)
	s.rpcClient = foregroundClient
	backgroundClient := connectAddressTestNode(t, address)
	s.backgroundRPCClient = backgroundClient
	for _, client := range []*rpcclient.RPCClient{foregroundClient, backgroundClient} {
		if info, err := client.GetBlockDAGInfo(); err != nil || info.VirtualDAAScore != activation {
			t.Fatalf("existing wallet stream could not fetch DAG info: %v, %v", info, err)
		}
	}

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	addresses, err := s.ShowAddresses(ctx, &pb.ShowAddressesRequest{AddressType: pb.AddressType_ADDRESS_TYPE_MLDSA44})
	if err != nil || len(addresses.GetAddress()) != 1 {
		t.Fatalf("explicit listing with both node slots occupied: %v, %v", addresses, err)
	}
}
