package server

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/daemon/pb"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/router"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/server/grpcserver/protowire"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/rpcclient"
	"github.com/HoosatNetwork/HTND/v2/util"
	"github.com/HoosatNetwork/HTND/v2/version"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// dagInfoNode is a node RPC server that reports virtualDAAScore, and counts how often it is asked.
type dagInfoNode struct {
	protowire.UnimplementedRPCServer
	virtualDAAScore atomic.Uint64
	dagInfoRequests atomic.Int32
	reportError     atomic.Bool
	releaseFirst    chan struct{}
	firstRequest    chan struct{}
}

func (n *dagInfoNode) MessageStream(stream protowire.RPC_MessageStreamServer) error {
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
		case *appmessage.GetBlockDAGInfoRequestMessage:
			info := &appmessage.GetBlockDAGInfoResponseMessage{VirtualDAAScore: n.virtualDAAScore.Load()}
			if n.reportError.Load() {
				info.Error = &appmessage.RPCError{Message: "DAG info unavailable"}
			}
			if n.dagInfoRequests.Add(1) == 1 && n.firstRequest != nil {
				close(n.firstRequest)
				select {
				case <-n.releaseFirst:
				case <-stream.Context().Done():
					return stream.Context().Err()
				}
			}
			response = info
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

// TestShowAddressesAndMLDSA44 pins two things about ShowAddresses.
//
// It shows ML-DSA-44 addresses only once ML-DSA-44 is active, as NewAddress does. It used to list
// them at any time, so an address could be copied and paid before the network accepts ML-DSA-44
// spends, and such coins cannot be spent until it does.
//
// And includeAll answers with the three secp256k1 forms of each address - P2PK, P2PKH, P2SH - as its
// description says and as it did before ML-DSA-44. It used to add the two ML-DSA-44 forms for every
// index the wallet has ML-DSA-44 keys for, unmarked, so the answer was five addresses for some
// indexes and three for others, and a caller reading it three by three got the wrong addresses.
func TestShowAddressesAndMLDSA44(t *testing.T) {
	w := newMLWallet(t, 1, 1) // mainnet; ML-DSA-44 keys for indexes 0-2
	s := w.s
	const usedIndexes = 2
	node := &dagInfoNode{}
	prepareAddressListings(t, s, usedIndexes, node)

	const beforeActivation, afterActivation = uint64(1_000), uint64(300_000_000)
	if libhtnwallet.MLDSA44Active(w.params, beforeActivation) || !libhtnwallet.MLDSA44Active(w.params, afterActivation) {
		t.Fatalf("the test's DAA scores are not on both sides of the ML-DSA-44 activation of %s", w.params.Name)
	}
	show := func(request *pb.ShowAddressesRequest) ([]string, error) {
		response, err := s.ShowAddresses(context.Background(), request)
		if err != nil {
			return nil, err
		}
		return response.Address, nil
	}
	mldsa44Types := []pb.AddressType{pb.AddressType_ADDRESS_TYPE_MLDSA44, pb.AddressType_ADDRESS_TYPE_MLDSA44_P2SH}

	// The wallet's ML-DSA-44 addresses, of both forms, at the used indexes.
	mldsa44Addresses := make(map[pb.AddressType][]string)
	isMLDSA44 := make(map[string]bool)
	for index := uint32(1); index <= usedIndexes; index++ {
		for addressType, form := range map[pb.AddressType]libhtnwallet.MLDSA44AddressForm{
			pb.AddressType_ADDRESS_TYPE_MLDSA44:      libhtnwallet.MLDSA44AddressFormP2PKH,
			pb.AddressType_ADDRESS_TYPE_MLDSA44_P2SH: libhtnwallet.MLDSA44AddressFormP2SH,
		} {
			address := w.address(t, &walletAddress{
				index: index, keyChain: libhtnwallet.ExternalKeychain, mldsa44: true, mldsa44Form: form,
			}).String()
			mldsa44Addresses[addressType] = append(mldsa44Addresses[addressType], address)
			isMLDSA44[address] = true
		}
	}

	node.virtualDAAScore.Store(beforeActivation)
	for _, addressType := range mldsa44Types {
		addresses, err := show(&pb.ShowAddressesRequest{AddressType: addressType})
		if err == nil || !strings.Contains(err.Error(), "not active") {
			t.Fatalf("before activation, %s addresses were shown: %v (err %v)", addressType, addresses, err)
		}
	}

	// includeAll and the secp256k1 address types answer as ever, before activation and after it, and
	// without asking the node.
	askedBefore := node.dagInfoRequests.Load()
	for _, virtualDAAScore := range []uint64{beforeActivation, afterActivation} {
		node.virtualDAAScore.Store(virtualDAAScore)
		// includeAll overrides the address type, an ML-DSA-44 one too.
		for _, addressType := range append([]pb.AddressType{pb.AddressType_ADDRESS_TYPE_UNSPECIFIED}, mldsa44Types...) {
			addresses, err := show(&pb.ShowAddressesRequest{IncludeAll: true, AddressType: addressType})
			if err != nil {
				t.Fatalf("ShowAddresses with includeAll: %+v", err)
			}
			if len(addresses) != 3*usedIndexes {
				t.Fatalf("includeAll answered %d addresses for %d used indexes, want three for each: %v",
					len(addresses), usedIndexes, addresses)
			}
			for i, address := range addresses {
				if isMLDSA44[address] {
					t.Fatalf("includeAll lists the ML-DSA-44 address %s", address)
				}
				decoded, err := util.DecodeAddress(address, w.params.Prefix)
				if err != nil {
					t.Fatalf("DecodeAddress: %+v", err)
				}
				// P2PK, P2PKH, P2SH for each index, in that order.
				var isWantedForm bool
				switch i % 3 {
				case 0:
					_, isWantedForm = decoded.(*util.AddressPublicKey)
				case 1:
					_, isWantedForm = decoded.(*util.AddressPublicKeyHash)
				case 2:
					_, isWantedForm = decoded.(*util.AddressScriptHash)
				}
				if !isWantedForm {
					t.Fatalf("includeAll address #%d is a %T; the order is P2PK, P2PKH, P2SH for each index", i, decoded)
				}
			}
		}
		for _, addressType := range []pb.AddressType{
			pb.AddressType_ADDRESS_TYPE_UNSPECIFIED, pb.AddressType_ADDRESS_TYPE_P2PK,
			pb.AddressType_ADDRESS_TYPE_P2PKH, pb.AddressType_ADDRESS_TYPE_P2SH,
		} {
			addresses, err := show(&pb.ShowAddressesRequest{AddressType: addressType})
			if err != nil || len(addresses) != usedIndexes {
				t.Fatalf("%s: got %d addresses (err %v), want %d", addressType, len(addresses), err, usedIndexes)
			}
		}
	}
	if asked := node.dagInfoRequests.Load() - askedBefore; asked != 0 {
		t.Fatalf("listing secp256k1 addresses asked the node for its DAG info %d times", asked)
	}

	// After activation the ML-DSA-44 addresses are listed by their address type, as before.
	node.virtualDAAScore.Store(afterActivation)
	for _, addressType := range mldsa44Types {
		addresses, err := show(&pb.ShowAddressesRequest{AddressType: addressType})
		if err != nil {
			t.Fatalf("after activation, %s addresses were refused: %+v", addressType, err)
		}
		if strings.Join(addresses, " ") != strings.Join(mldsa44Addresses[addressType], " ") {
			t.Fatalf("%s: got %v, want %v", addressType, addresses, mldsa44Addresses[addressType])
		}
	}
}

func setAddressListingState(t *testing.T, s *server, usedIndexes uint32) {
	t.Helper()
	if err := s.keysFile.SetLastUsedExternalIndex(usedIndexes); err != nil {
		t.Fatalf("SetLastUsedExternalIndex: %+v", err)
	}
	s.nextSyncStartIndex = 1000
	s.firstSyncDone.Store(true)
	s.addressListingTimeout = maxAddressListingRPCTimeout
}

func serveAddressTestNode(t *testing.T, node protowire.RPCServer) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	grpcServer := grpc.NewServer()
	protowire.RegisterRPCServer(grpcServer, node)
	go func() { _ = grpcServer.Serve(listener) }()
	t.Cleanup(grpcServer.Stop)
	return listener.Addr().String()
}

func connectAddressTestNode(t *testing.T, address string) *rpcclient.RPCClient {
	t.Helper()
	client, err := rpcclient.NewRPCClient(address)
	if err != nil {
		t.Fatalf("NewRPCClient: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	client.SetTimeout(5 * time.Second)
	return client
}

func prepareAddressListings(t *testing.T, s *server, usedIndexes uint32, node *dagInfoNode) {
	t.Helper()
	setAddressListingState(t, s, usedIndexes)
	s.rpcClient = connectAddressTestNode(t, serveAddressTestNode(t, node))
}

func TestShowAddressesWithheldDAGInfoDoesNotBlockWallet(t *testing.T) {
	s := newMLWallet(t, 1, 1).s
	node := &dagInfoNode{firstRequest: make(chan struct{}), releaseFirst: make(chan struct{})}
	node.virtualDAAScore.Store(300_000_000)
	prepareAddressListings(t, s, 1, node)
	wait, stopWaiting := context.WithTimeout(t.Context(), 5*time.Second)
	defer stopWaiting()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	mldsaRequest := &pb.ShowAddressesRequest{AddressType: pb.AddressType_ADDRESS_TYPE_MLDSA44}
	first := make(chan error, 1)
	go func() {
		_, err := s.ShowAddresses(ctx, mldsaRequest)
		first <- err
	}()
	select {
	case <-node.firstRequest:
	case err := <-first:
		t.Fatalf("listing returned without querying activation: %v", err)
	case <-wait.Done():
		t.Fatal("the node did not receive the activation query")
	}

	if !s.lock.TryLock() {
		t.Fatal("the activation query is holding the wallet lock")
	}
	s.lock.Unlock()
	result, err := s.ShowAddresses(wait, &pb.ShowAddressesRequest{})
	if err != nil || len(result.GetAddress()) != 1 {
		t.Fatalf("legacy address listing during activation query: %v, %v", result, err)
	}

	cancel()
	select {
	case err := <-first:
		if status.Code(err) != codes.Canceled {
			t.Fatalf("canceled listing returned %v", err)
		}
	case <-wait.Done():
		t.Fatal("the canceled listing did not return")
	}

	// The withheld reply reports activation. The next request must check its own,
	// pre-activation reply after the first caller has already left.
	node.virtualDAAScore.Store(1_000)
	close(node.releaseFirst)
	if result, err := s.ShowAddresses(wait, mldsaRequest); err == nil || !strings.Contains(err.Error(), "not active") {
		t.Fatalf("second activation query: %v, %v", result, err)
	}
	if node.dagInfoRequests.Load() != 2 {
		t.Fatalf("node received %d DAG-info requests, want 2", node.dagInfoRequests.Load())
	}
}

func TestShowAddressesDAGInfoDeadline(t *testing.T) {
	s := newMLWallet(t, 1, 1).s
	node := &dagInfoNode{firstRequest: make(chan struct{})}
	prepareAddressListings(t, s, 1, node)
	s.addressListingTimeout = 100 * time.Millisecond

	result, err := s.ShowAddresses(t.Context(), &pb.ShowAddressesRequest{
		AddressType: pb.AddressType_ADDRESS_TYPE_MLDSA44_P2SH,
	})
	if status.Code(err) != codes.DeadlineExceeded || result != nil {
		t.Fatalf("listing with no node reply returned %v, %v", result, err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	result, err = s.ShowAddresses(ctx, &pb.ShowAddressesRequest{AddressType: pb.AddressType_ADDRESS_TYPE_MLDSA44})
	if status.Code(err) != codes.DeadlineExceeded || result != nil || node.dagInfoRequests.Load() != 1 {
		t.Fatalf("listing waiting for the previous node reply returned %v, %v after %d queries",
			result, err, node.dagInfoRequests.Load())
	}
}

func TestShowAddressesRecoversAfterNodeDAGInfoTimeout(t *testing.T) {
	s := newMLWallet(t, 1, 1).s
	node := &dagInfoNode{firstRequest: make(chan struct{})}
	prepareAddressListings(t, s, 1, node)
	s.rpcClient.(*rpcclient.RPCClient).SetTimeout(150 * time.Millisecond)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	_, err := s.getBlockDAGInfo(ctx)
	if !errors.Is(err, router.ErrTimeout) {
		t.Fatalf("withheld node reply: got %v, want node timeout", err)
	}

	// The first stream still owes a pre-activation reply. The next listing
	// can only succeed by getting a fresh reply on a replacement stream.
	node.virtualDAAScore.Store(258_000_000)
	result, err := s.ShowAddresses(ctx, &pb.ShowAddressesRequest{AddressType: pb.AddressType_ADDRESS_TYPE_MLDSA44})
	if err != nil || len(result.GetAddress()) != 1 || node.dagInfoRequests.Load() != 2 {
		t.Fatalf("listing after node timeout: %v, %v after %d queries", result, err, node.dagInfoRequests.Load())
	}
}

func TestShowAddressesEmptyMLDSA44ListNeedsNoNode(t *testing.T) {
	for _, tc := range []struct {
		name        string
		wallet      func(*testing.T) *feeWallet
		usedIndexes uint32
	}{
		{"empty history", func(t *testing.T) *feeWallet { return newMLWallet(t, 1, 1) }, 0},
		{"no ML-DSA-44 pool", newSchnorrWallet, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.wallet(t).s
			node := &dagInfoNode{}
			prepareAddressListings(t, s, tc.usedIndexes, node)
			result, err := s.ShowAddresses(t.Context(), &pb.ShowAddressesRequest{
				AddressType: pb.AddressType_ADDRESS_TYPE_MLDSA44,
			})
			if err != nil || result == nil || len(result.GetAddress()) != 0 || node.dagInfoRequests.Load() != 0 {
				t.Fatalf("empty ML-DSA-44 listing: %v, %v, node queries %d", result, err, node.dagInfoRequests.Load())
			}
		})
	}
}

func TestShowAddressesMultisigActivation(t *testing.T) {
	w := newMLWallet(t, 2, 2)
	s := w.s
	node := &dagInfoNode{}
	prepareAddressListings(t, s, 1, node)
	legacy, err := s.ShowAddresses(t.Context(), &pb.ShowAddressesRequest{IncludeAll: true})
	if err != nil || len(legacy.GetAddress()) != 1 || node.dagInfoRequests.Load() != 0 {
		t.Fatalf("legacy multisig listing: %v, %v, node queries %d", legacy, err, node.dagInfoRequests.Load())
	}
	want := w.address(t, &walletAddress{index: 1, keyChain: libhtnwallet.ExternalKeychain, mldsa44: true}).String()
	for _, typ := range []pb.AddressType{pb.AddressType_ADDRESS_TYPE_MLDSA44, pb.AddressType_ADDRESS_TYPE_MLDSA44_P2SH} {
		request := &pb.ShowAddressesRequest{IncludeAll: true, AddressType: typ}
		node.virtualDAAScore.Store(1_000)
		if result, err := s.ShowAddresses(t.Context(), request); err == nil || !strings.Contains(err.Error(), "not active") {
			t.Fatalf("multisig listing for %s before activation: %v, %v", typ, result, err)
		}
		node.virtualDAAScore.Store(300_000_000)
		result, err := s.ShowAddresses(t.Context(), request)
		if err != nil || len(result.GetAddress()) != 1 || result.Address[0] != want {
			t.Fatalf("multisig listing for %s after activation: %v, %v, want %s", typ, result, err, want)
		}
	}
}

func TestAddressActivationStatusOverWalletGRPC(t *testing.T) {
	s := newMLWallet(t, 1, 1).s
	node := &dagInfoNode{}
	node.virtualDAAScore.Store(257_999_999)
	prepareAddressListings(t, s, 1, node)

	listener := bufconn.Listen(1024 * 1024)
	grpcServer := grpc.NewServer()
	pb.RegisterHtnwalletdServer(grpcServer, s)
	go func() { _ = grpcServer.Serve(listener) }()
	t.Cleanup(grpcServer.Stop)
	connection, err := grpc.NewClient("passthrough:///wallet",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = connection.Close() })

	client := pb.NewHtnwalletdClient(connection)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	request := &pb.ShowAddressesRequest{AddressType: pb.AddressType_ADDRESS_TYPE_MLDSA44}
	for _, tc := range []struct {
		message   string
		nodeError bool
	}{
		{"not active", false},
		{"DAG info unavailable", true},
	} {
		node.reportError.Store(tc.nodeError)
		_, err := client.NewAddress(ctx, &pb.NewAddressRequest{AddressType: request.AddressType})
		if status.Code(err) != codes.Unknown || !strings.Contains(err.Error(), tc.message) {
			t.Fatalf("NewAddress: got %v, want Unknown with %q", err, tc.message)
		}
		_, err = client.ShowAddresses(ctx, request)
		if status.Code(err) != codes.Unknown || !strings.Contains(err.Error(), tc.message) {
			t.Fatalf("ShowAddresses: got %v, want Unknown with %q", err, tc.message)
		}
	}
}
