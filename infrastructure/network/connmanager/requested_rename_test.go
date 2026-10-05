package connmanager

import (
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/infrastructure/config"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/router"
)

func freeLocalAddress(t *testing.T) string {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %+v", err)
	}
	defer listener.Close()
	return listener.Addr().String()
}

func startNetAdapterForTest(t *testing.T, address string) *netadapter.NetAdapter {
	cfg := config.DefaultConfig()
	cfg.Listeners = []string{address}
	adapter, err := netadapter.NewNetAdapter(cfg)
	if err != nil {
		t.Fatalf("NewNetAdapter: %+v", err)
	}
	adapter.SetP2PRouterInitializer(func(*router.Router, *netadapter.NetConnection) {})
	adapter.SetRPCRouterInitializer(func(*router.Router, *netadapter.NetConnection) {})
	if err := adapter.Start(); err != nil {
		t.Fatalf("Start: %+v", err)
	}
	t.Cleanup(func() { _ = adapter.Stop() })
	return adapter
}

// TestRequestedConnectionMatchedByResolvedAddressStaysActive pins that a requested peer given by hostname stays
// tracked once it is matched to its live IP:port connection. The request used to be renamed inside the range over
// activeRequested; Go visits a key added during range (in practice once the map holds more than 8 requests), and by
// then the connection had been removed from the connection set, so the renamed request was dropped as disconnected -
// and a permanent one was redialed at once.
func TestRequestedConnectionMatchedByResolvedAddressStaysActive(t *testing.T) {
	adapter := startNetAdapterForTest(t, freeLocalAddress(t))
	peerAddress := freeLocalAddress(t)
	startNetAdapterForTest(t, peerAddress)

	if err := adapter.P2PConnect(peerAddress); err != nil {
		t.Fatalf("P2PConnect: %+v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for adapter.P2PConnectionCount() != 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	connections := adapter.P2PConnections()
	if len(connections) != 1 {
		t.Fatalf("expected one live connection, got %d", len(connections))
	}
	liveAddress := connections[0].Address()
	_, port, err := net.SplitHostPort(liveAddress)
	if err != nil {
		t.Fatalf("SplitHostPort(%q): %+v", liveAddress, err)
	}
	requestedAddress := fmt.Sprintf("peer.test:%s", port)

	connectionManager := &ConnectionManager{
		cfg: &config.Config{
			Lookup: func(host string) ([]net.IP, error) {
				if host == "peer.test" {
					return []net.IP{net.ParseIP("127.0.0.1")}, nil
				}
				return nil, nil
			},
		},
		netAdapter: adapter,
	}

	const attempts = 300
	for attempt := range attempts {
		connectionManager.activeRequested = map[string]*connectionRequest{
			requestedAddress: {address: requestedAddress},
		}
		// Go's runtime only visits keys added during range once a map outgrows a single group (more than 8
		// entries), so the other requests - one-try peers that are not connected and are simply dropped - make
		// the rename observable.
		for i := range 8 {
			unreachable := fmt.Sprintf("unreachable%d.test:%s", i, port)
			connectionManager.activeRequested[unreachable] = &connectionRequest{address: unreachable}
		}
		connectionManager.pendingRequested = map[string]*connectionRequest{}

		connectionManager.checkRequestedConnections(convertToSet(adapter.P2PConnections()))

		request, ok := connectionManager.activeRequested[liveAddress]
		if !ok || len(connectionManager.activeRequested) != 1 || len(connectionManager.pendingRequested) != 0 {
			t.Fatalf("attempt %d: the requested connection was dropped although it is live: active=%v pending=%v",
				attempt, keys(connectionManager.activeRequested), keys(connectionManager.pendingRequested))
		}
		if request.address != liveAddress {
			t.Fatalf("attempt %d: request address is %q, want %q", attempt, request.address, liveAddress)
		}
	}
}

func keys(requests map[string]*connectionRequest) []string {
	result := make([]string, 0, len(requests))
	for key := range requests {
		result = append(result, key)
	}
	return result
}
