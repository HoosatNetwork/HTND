package protocol

import (
	"net"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/config"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter"
	routerpkg "github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/router"
	"github.com/pkg/errors"
)

// TestHandleErrorLocalErrorDisconnectsWithoutPanic gives Manager.handleError an error that is neither a protocol error
// nor one of the known disconnect causes, as a local database failure is. It used to end in panic(err), which stopped
// the node. It must now disconnect the peer and return. The Manager has no context, so a ban attempt, which reads the
// context's config, would panic too: the test also pins that a local error does not ban.
func TestHandleErrorLocalErrorDisconnectsWithoutPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("handleError panicked on a local error: %v", r)
		}
	}()

	netConnection, route := loopbackNetConnectionWithRoute(t)
	if err := route.Enqueue(appmessage.NewMsgVerAck()); err != nil {
		t.Fatalf("the route of a fresh connection is closed: %+v", err)
	}

	localErr := errors.New("local database failure")
	(&Manager{}).handleError(localErr, netConnection, nil)

	if netConnection.ErrorMessage != localErr {
		t.Fatalf("ErrorMessage = %v, want the error passed in", netConnection.ErrorMessage)
	}
	// NetConnection.Disconnect closes the connection's router, and with it every route.
	if err := route.Enqueue(appmessage.NewMsgVerAck()); !errors.Is(err, routerpkg.ErrRouteClosed) {
		t.Fatalf("the connection was not disconnected (enqueue err = %v)", err)
	}
}

// loopbackNetConnectionWithRoute returns the connecting side's NetConnection of a real loopback connection between
// two net adapters, which nothing reads from or writes to, and an incoming route of its router that closes when the
// connection is disconnected.
func loopbackNetConnectionWithRoute(t *testing.T) (*netadapter.NetConnection, *routerpkg.Route) {
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
	listening.SetP2PRouterInitializer(func(*routerpkg.Router, *netadapter.NetConnection) {})
	listening.SetRPCRouterInitializer(func(*routerpkg.Router, *netadapter.NetConnection) {})
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
	type connectionAndRoute struct {
		connection *netadapter.NetConnection
		route      *routerpkg.Route
	}
	connections := make(chan connectionAndRoute, 1)
	dialing.SetP2PRouterInitializer(func(router *routerpkg.Router, netConnection *netadapter.NetConnection) {
		route, err := router.AddIncomingRoute("test", []appmessage.MessageCommand{appmessage.CmdVerAck})
		if err != nil {
			t.Error(err)
		}
		connections <- connectionAndRoute{netConnection, route}
	})
	dialing.SetRPCRouterInitializer(func(*routerpkg.Router, *netadapter.NetConnection) {})
	if err := dialing.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dialing.Stop() })
	if err := dialing.P2PConnect(address); err != nil {
		t.Fatal(err)
	}
	c := <-connections
	return c.connection, c.route
}
