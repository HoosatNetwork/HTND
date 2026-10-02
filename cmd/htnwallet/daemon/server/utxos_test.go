package server

import (
	"context"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/daemon/pb"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/keys"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/server/grpcserver/protowire"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/rpcclient"
	"github.com/HoosatNetwork/HTND/v2/version"
	"google.golang.org/grpc"
)

// utxosByAddressesNode is a node RPC server that holds one coin on each address in coins, of that amount.
type utxosByAddressesNode struct {
	protowire.UnimplementedRPCServer
	coins map[string]uint64
}

func (n *utxosByAddressesNode) MessageStream(stream protowire.RPC_MessageStreamServer) error {
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
		switch message := appMessage.(type) {
		case *appmessage.GetInfoRequestMessage:
			response = appmessage.NewGetInfoResponseMessage("", 0, version.Version(), false, true, true, 0, 0, "")
		case *appmessage.GetUTXOsByAddressesRequestMessage:
			var entries []*appmessage.UTXOsByAddressesEntry
			for _, address := range message.Addresses {
				if amount, ok := n.coins[address]; ok {
					entries = append(entries, &appmessage.UTXOsByAddressesEntry{
						Address:  address,
						Outpoint: &appmessage.RPCOutpoint{TransactionID: strings.Repeat("ab", 32), Index: 1},
						UTXOEntry: &appmessage.RPCUTXOEntry{
							Amount:          amount,
							ScriptPublicKey: &appmessage.RPCScriptPublicKey{Script: "51"},
							BlockDAAScore:   7,
						},
					})
				}
			}
			response = appmessage.NewGetUTXOsByAddressesResponseMessage(entries)
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

// TestGetUTXOsLabelsEachCoinWithItsDerivationPath pins that GetUTXOs returns the coins of every tracked
// address - receive and change, in whichever address form they were paid to - each with the address and
// the path that address is derived at.
func TestGetUTXOsLabelsEachCoinWithItsDerivationPath(t *testing.T) {
	params := &dagconfig.TestnetParams
	mnemonic, err := libhtnwallet.CreateMnemonic()
	if err != nil {
		t.Fatalf("CreateMnemonic: %+v", err)
	}
	extendedPublicKey, err := libhtnwallet.MasterPublicKeyFromMnemonic(params, mnemonic, false)
	if err != nil {
		t.Fatalf("MasterPublicKeyFromMnemonic: %+v", err)
	}
	keysFile := &keys.File{ExtendedPublicKeys: []string{extendedPublicKey}, MinimumSignatures: 1}
	if err := keysFile.SetPath(params, filepath.Join(t.TempDir(), "keys.json"), true); err != nil {
		t.Fatalf("SetPath: %+v", err)
	}
	s := &server{params: params, keysFile: keysFile, addressSet: make(walletAddressSet), nextSyncStartIndex: 1000}
	s.firstSyncDone.Store(true)

	// The scan tracks every form of an address; the node holds a coin on one form of each.
	coins := map[string]uint64{}
	wantPaths := map[string]string{}
	for _, tracked := range []struct {
		address *walletAddress
		form    int
		amount  uint64
	}{
		{&walletAddress{index: 2, keyChain: libhtnwallet.ExternalKeychain}, 0, 100},
		{&walletAddress{index: 5, keyChain: libhtnwallet.InternalKeychain}, 1, 200},
	} {
		addressStrings, err := s.walletAddressStringsForScan(tracked.address)
		if err != nil {
			t.Fatalf("walletAddressStringsForScan: %+v", err)
		}
		for _, addressString := range addressStrings {
			s.addressSet[addressString] = tracked.address
		}
		coins[addressStrings[tracked.form]] = tracked.amount
		wantPaths[addressStrings[tracked.form]] = s.walletAddressPath(tracked.address)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %+v", err)
	}
	grpcServer := grpc.NewServer()
	protowire.RegisterRPCServer(grpcServer, &utxosByAddressesNode{coins: coins})
	go func() { _ = grpcServer.Serve(listener) }()
	t.Cleanup(grpcServer.Stop)
	s.rpcClient, err = rpcclient.NewRPCClient(listener.Addr().String())
	if err != nil {
		t.Fatalf("NewRPCClient: %+v", err)
	}
	t.Cleanup(func() { _ = s.rpcClient.Close() })
	s.rpcClient.SetTimeout(5 * time.Second)

	response, err := s.GetUTXOs(context.Background(), &pb.GetUTXOsRequest{})
	if err != nil {
		t.Fatalf("GetUTXOs: %+v", err)
	}
	if len(response.Utxos) != len(wantPaths) {
		t.Fatalf("got %d UTXOs, want %d", len(response.Utxos), len(wantPaths))
	}
	for _, utxo := range response.Utxos {
		if want := wantPaths[utxo.Address]; utxo.DerivationPath != want {
			t.Fatalf("coin of %s reported at path %q, want %q", utxo.Address, utxo.DerivationPath, want)
		}
		if utxo.UtxoEntry.Amount != coins[utxo.Address] || utxo.Outpoint.Index != 1 {
			t.Fatalf("coin of %s came back as %+v", utxo.Address, utxo)
		}
	}
}
