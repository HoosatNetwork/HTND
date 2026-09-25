package server

import (
	"net"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/keys"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/server/grpcserver/protowire"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/rpcclient"
	"github.com/HoosatNetwork/HTND/v2/version"
	"google.golang.org/grpc"
)

// getInfoOnlyRPCServer answers GetInfo, which is all an RPC client needs to connect.
type getInfoOnlyRPCServer struct {
	protowire.UnimplementedRPCServer
}

func (s *getInfoOnlyRPCServer) MessageStream(stream protowire.RPC_MessageStreamServer) error {
	for {
		request, err := stream.Recv()
		if err != nil {
			return nil
		}
		appMessage, err := request.ToAppMessage()
		if err != nil {
			return err
		}
		if _, ok := appMessage.(*appmessage.GetInfoRequestMessage); !ok {
			continue
		}
		response, err := protowire.FromAppMessage(appmessage.NewGetInfoResponseMessage(
			"", 0, version.Version(), false, true, true, 0, 0, ""))
		if err != nil {
			return err
		}
		if err := stream.Send(response); err != nil {
			return nil
		}
	}
}

// closedRPCClient returns a client whose routes are closed, as they are while it reconnects.
func closedRPCClient(t *testing.T) *rpcclient.RPCClient {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %+v", err)
	}
	grpcServer := grpc.NewServer()
	protowire.RegisterRPCServer(grpcServer, &getInfoOnlyRPCServer{})
	go func() { _ = grpcServer.Serve(listener) }()
	t.Cleanup(grpcServer.Stop)

	client, err := rpcclient.NewRPCClient(listener.Addr().String())
	if err != nil {
		t.Fatalf("NewRPCClient: %+v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("Close: %+v", err)
	}
	return client
}

// TestAddressScansSkippedOnClosedRouteAreRetried pins that an address scan skipped because the RPC route was
// closed does not count as done. collectFarAddresses used to advance nextSyncStartIndex past the skipped
// batch anyway, so those indexes were never scanned by the far scan again, and collectRecentAddresses marked
// its whole range as scanned.
func TestAddressScansSkippedOnClosedRouteAreRetried(t *testing.T) {
	params := &dagconfig.TestnetParams
	mnemonic, err := libhtnwallet.CreateMnemonic()
	if err != nil {
		t.Fatalf("CreateMnemonic: %+v", err)
	}
	extendedPublicKey, err := libhtnwallet.MasterPublicKeyFromMnemonic(params, mnemonic, false)
	if err != nil {
		t.Fatalf("MasterPublicKeyFromMnemonic: %+v", err)
	}

	s := &server{
		params:              params,
		keysFile:            &keys.File{ExtendedPublicKeys: []string{extendedPublicKey}, MinimumSignatures: 1},
		backgroundRPCClient: closedRPCClient(t),
		addressSet:          make(walletAddressSet),
	}

	if err := s.collectFarAddresses(); err != nil {
		t.Fatalf("collectFarAddresses: %+v", err)
	}
	if s.nextSyncStartIndex != 0 {
		t.Fatalf("a skipped far scan advanced nextSyncStartIndex to %d", s.nextSyncStartIndex)
	}

	if err := s.collectRecentAddresses(); err != nil {
		t.Fatalf("collectRecentAddresses: %+v", err)
	}
	if s.nextSyncStartIndex != 0 {
		t.Fatalf("a skipped recent scan advanced nextSyncStartIndex to %d", s.nextSyncStartIndex)
	}
}
