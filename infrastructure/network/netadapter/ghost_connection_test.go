package netadapter

import (
	"net"
	"sync/atomic"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/router"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/server"
)

// disconnectableConnection behaves like a gRPC connection for the adapter's bookkeeping: Disconnect runs
// the disconnected handler once, and starting a connection that is already disconnected does nothing.
type disconnectableConnection struct {
	connected      atomic.Bool
	outbound       bool
	onDisconnected server.OnDisconnectedHandler
}

func (c *disconnectableConnection) String() string       { return "disconnectable" }
func (c *disconnectableConnection) Start(*router.Router) {}
func (c *disconnectableConnection) IsConnected() bool    { return c.connected.Load() }
func (c *disconnectableConnection) IsOutbound() bool     { return c.outbound }
func (c *disconnectableConnection) Address() *net.TCPAddr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 16111}
}
func (c *disconnectableConnection) SetOnInvalidMessageHandler(server.OnInvalidMessageHandler) {}
func (c *disconnectableConnection) SetOnDisconnectedHandler(handler server.OnDisconnectedHandler) {
	c.onDisconnected = handler
}
func (c *disconnectableConnection) Disconnect() {
	if c.connected.CompareAndSwap(true, false) && c.onDisconnected != nil {
		c.onDisconnected()
	}
}

// TestConnectionDisconnectedDuringInitializationIsNotKept pins that a P2P connection disconnected while
// its router is being initialized - the protocol's ban check does that - is not kept by the adapter. It
// used to be added to p2pConnections and started after it was already gone, so its disconnected handler
// never ran and the entry, and for an outbound peer its cached router, stayed forever.
func TestConnectionDisconnectedDuringInitializationIsNotKept(t *testing.T) {
	for _, outbound := range []bool{false, true} {
		na := &NetAdapter{
			p2pConnections:     make(map[*NetConnection]struct{}),
			outboundP2PRouters: make(map[string]*router.Router),
		}
		na.SetP2PRouterInitializer(func(_ *router.Router, netConnection *NetConnection) {
			netConnection.Disconnect()
		})

		connection := &disconnectableConnection{outbound: outbound}
		connection.connected.Store(true)
		if err := na.onP2PConnectedHandler(connection); err != nil {
			t.Fatalf("onP2PConnectedHandler: %+v", err)
		}

		if count := na.P2PConnectionCount(); count != 0 {
			t.Fatalf("outbound=%t: a connection disconnected during initialization is still registered (%d connections)",
				outbound, count)
		}
		if len(na.outboundP2PRouters) != 0 {
			t.Fatalf("outbound=%t: the router of a connection disconnected during initialization is still cached", outbound)
		}
	}
}
