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

// banRPCServer answers GetInfo, Ban and Unban requests.
type banRPCServer struct {
	protowire.UnimplementedRPCServer
}

func (s *banRPCServer) MessageStream(stream protowire.RPC_MessageStreamServer) error {
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
		case *appmessage.BanRequestMessage:
			response = appmessage.NewBanResponseMessage()
		case *appmessage.UnbanRequestMessage:
			response = appmessage.NewUnbanResponseMessage()
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

// TestBanAndUnbanReadTheirResponses pins that Ban and Unban wait on the routes their responses arrive on. They used
// to dequeue from the request routes, which never receive anything, so both always timed out even though the node
// had applied the ban.
func TestBanAndUnbanReadTheirResponses(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %+v", err)
	}
	server := grpc.NewServer()
	protowire.RegisterRPCServer(server, &banRPCServer{})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	client, err := NewRPCClient(listener.Addr().String())
	if err != nil {
		t.Fatalf("NewRPCClient: %+v", err)
	}
	defer client.Close()
	client.SetTimeout(3 * time.Second)

	if _, err := client.Ban("127.0.0.2"); err != nil {
		t.Fatalf("Ban: %+v", err)
	}
	if _, err := client.Unban("127.0.0.2"); err != nil {
		t.Fatalf("Unban: %+v", err)
	}
}
