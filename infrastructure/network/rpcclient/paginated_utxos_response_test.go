package rpcclient

import (
	"net"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/server/grpcserver/protowire"
	"github.com/HoosatNetwork/HTND/v2/version"
	"google.golang.org/grpc"
)

// paginatedUTXOsRPCServer answers GetInfo and GetPaginatedUTXOsByAddresses requests.
type paginatedUTXOsRPCServer struct {
	protowire.UnimplementedRPCServer
}

func (s *paginatedUTXOsRPCServer) MessageStream(stream protowire.RPC_MessageStreamServer) error {
	for {
		request, err := stream.Recv()
		if err != nil {
			return nil
		}
		appMessage, err := request.ToAppMessage()
		if err != nil {
			return err
		}
		var response appmessage.Message
		switch appMessage.(type) {
		case *appmessage.GetInfoRequestMessage:
			response = appmessage.NewGetInfoResponseMessage("", 0, version.Version(), false, true, true, 0, 0, "")
		case *appmessage.GetPaginatedUTXOsByAddressesRequestMessage:
			response = appmessage.NewGetPaginatedUTXOsByAddressesResponseMessage([]*appmessage.UTXOsByAddressesEntry{{
				Address:   "hoosat:test",
				Outpoint:  &appmessage.RPCOutpoint{TransactionID: "00", Index: 1},
				UTXOEntry: &appmessage.RPCUTXOEntry{Amount: 7, ScriptPublicKey: &appmessage.RPCScriptPublicKey{}},
			}})
		default:
			continue
		}
		protoResponse, err := protowire.FromAppMessage(response)
		if err != nil {
			return err
		}
		if err := stream.Send(protoResponse); err != nil {
			return nil
		}
	}
}

// TestGetPaginatedUTXOsByAddressesResponseType pins that the client reads the paginated response as the message
// type the server sends for it. It used to assert the non-paginated GetUTXOsByAddresses response type, so every
// successful call panicked in the caller's process.
func TestGetPaginatedUTXOsByAddressesResponseType(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %+v", err)
	}
	server := grpc.NewServer()
	protowire.RegisterRPCServer(server, &paginatedUTXOsRPCServer{})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	client, err := NewRPCClient(listener.Addr().String())
	if err != nil {
		t.Fatalf("NewRPCClient: %+v", err)
	}
	defer client.Close()
	client.SetTimeout(5 * time.Second)

	response, err := client.GetPaginatedUTXOsByAddresses([]string{"hoosat:test"}, 0, 10)
	if err != nil {
		t.Fatalf("GetPaginatedUTXOsByAddresses: %+v", err)
	}
	if len(response.Entries) != 1 || response.Entries[0].UTXOEntry.Amount != 7 {
		t.Fatalf("unexpected entries: %+v", response.Entries)
	}
}
