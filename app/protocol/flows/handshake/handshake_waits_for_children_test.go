package handshake

import (
	"net"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/HoosatNetwork/HTND/v2/domain"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/config"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database/pebble"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/addressmanager"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/id"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/router"
)

// heldConsensus holds GetVirtualDAAScore until released, as a consensus call waiting for the consensus lock does.
type heldConsensus struct {
	externalapi.Consensus
	entered, release chan struct{}
}

func (c *heldConsensus) GetVirtualDAAScore() (uint64, error) {
	close(c.entered)
	<-c.release
	// What a node that is shutting down returns doesn't matter here; this one ends the handshake quietly.
	return 0, router.ErrRouteClosed
}

type heldDomain struct {
	domain.Domain
	consensus externalapi.Consensus
}

func (d heldDomain) Consensus() externalapi.Consensus { return d.consensus }

type handshakeContext struct {
	HandleHandshakeContext
	cfg            *config.Config
	netAdapter     *netadapter.NetAdapter
	addressManager *addressmanager.AddressManager
	domain         domain.Domain
}

func (c handshakeContext) Config() *config.Config                         { return c.cfg }
func (c handshakeContext) NetAdapter() *netadapter.NetAdapter             { return c.netAdapter }
func (c handshakeContext) AddressManager() *addressmanager.AddressManager { return c.addressManager }
func (c handshakeContext) Domain() domain.Domain                          { return c.domain }

// loopbackNetConnection returns a NetConnection of a real loopback connection between two net adapters, which
// nothing reads from or writes to.
func loopbackNetConnection(t *testing.T) (*netadapter.NetConnection, *netadapter.NetAdapter) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()

	listeningCfg := config.DefaultConfig()
	listeningCfg.Listeners = []string{address}
	listening, err := netadapter.NewNetAdapter(listeningCfg)
	if err != nil {
		t.Fatal(err)
	}
	listening.SetP2PRouterInitializer(func(*router.Router, *netadapter.NetConnection) {})
	listening.SetRPCRouterInitializer(func(*router.Router, *netadapter.NetConnection) {})
	if err := listening.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listening.Stop() })

	dialingCfg := config.DefaultConfig()
	dialingCfg.Listeners = nil
	dialing, err := netadapter.NewNetAdapter(dialingCfg)
	if err != nil {
		t.Fatal(err)
	}
	connections := make(chan *netadapter.NetConnection, 1)
	dialing.SetP2PRouterInitializer(func(_ *router.Router, netConnection *netadapter.NetConnection) {
		connections <- netConnection
	})
	dialing.SetRPCRouterInitializer(func(*router.Router, *netadapter.NetConnection) {})
	if err := dialing.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dialing.Stop() })
	if err := dialing.P2PConnect(address); err != nil {
		t.Fatal(err)
	}
	return <-connections, dialing
}

// TestHandshakeWaitsForBothGoroutines fails SendVersion, as a connection closed at shutdown does, while ReceiveVersion
// is inside a consensus call. HandleHandshake must not return before that call has: once it returns, the protocol
// manager no longer waits for the connection, and the node closes the database after the protocol manager's Close.
func TestHandshakeWaitsForBothGoroutines(t *testing.T) {
	netConnection, netAdapter := loopbackNetConnection(t)

	cfg := config.DefaultConfig()
	db, err := pebble.OpenPebbleDB(t.TempDir(), 8)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	addressManager, err := addressmanager.New(addressmanager.NewConfig(cfg), db)
	if err != nil {
		t.Fatal(err)
	}
	consensus := &heldConsensus{entered: make(chan struct{}), release: make(chan struct{})}
	context := handshakeContext{cfg: cfg, netAdapter: netAdapter, addressManager: addressManager,
		domain: heldDomain{consensus: consensus}}

	receiveVersionRoute, sendVersionRoute := router.NewRoute("receive version"), router.NewRoute("send version")
	outgoingRoute := router.NewRoute("outgoing")
	peerID, err := id.GenerateID()
	if err != nil {
		t.Fatal(err)
	}
	err = receiveVersionRoute.Enqueue(appmessage.NewMsgVersion(nil, peerID, cfg.ActiveNetParams.Name, nil, cfg.ProtocolVersion))
	if err != nil {
		t.Fatal(err)
	}

	returned := make(chan struct{})
	go func() {
		defer close(returned)
		_, _ = HandleHandshake(context, netConnection, receiveVersionRoute, sendVersionRoute, outgoingRoute)
	}()
	select {
	case <-consensus.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("ReceiveVersion did not reach its consensus call")
	}

	sendVersionRoute.Close()
	select {
	case <-returned:
		close(consensus.release)
		t.Fatal("HandleHandshake returned while ReceiveVersion was still inside a consensus call")
	case <-time.After(200 * time.Millisecond):
	}

	close(consensus.release)
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("HandleHandshake did not return after both of its goroutines finished")
	}
}
