package netadapter

import (
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/config"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/id"
	routerpkg "github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/router"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/server"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/server/grpcserver"
	"github.com/pkg/errors"
)

// RouterInitializer is a function that initializes a new
// router to be used with a new connection
type RouterInitializer func(*routerpkg.Router, *NetConnection)

// NetAdapter is an abstraction layer over networking.
// This type expects a RouteInitializer function. This
// function weaves together the various "routes" (messages
// and message handlers) without exposing anything related
// to networking internals.
type NetAdapter struct {
	cfg                  *config.Config
	id                   *id.ID
	p2pServer            server.P2PServer
	p2pRouterInitializer RouterInitializer
	rpcServer            server.Server
	rpcRouterInitializer RouterInitializer
	stop                 atomic.Uint32

	p2pConnections     map[*NetConnection]struct{}
	p2pConnectionsLock sync.RWMutex

	// outboundP2PRouters caches routers for outbound peers so reconnects can
	// reuse their buffered routes instead of allocating new ones.
	// Inbound peers are intentionally not cached to avoid unbounded memory growth.
	outboundP2PRouters     map[string]*routerpkg.Router
	outboundP2PRoutersLock sync.Mutex
}

// NewNetAdapter creates and starts a new NetAdapter on the
// given listeningPort
func NewNetAdapter(cfg *config.Config) (*NetAdapter, error) {
	netAdapterID, err := id.GenerateID()
	if err != nil {
		return nil, err
	}
	// HTN-166: must happen before either server is built, since gRPC captures the limits at
	// construction. Both default to 0, meaning "leave the built-in ceiling alone".
	err = grpcserver.SetMaxMessageSizes(cfg.P2PMaxMessageSize, cfg.RPCMaxMessageSize)
	if err != nil {
		return nil, err
	}

	p2pServer, err := grpcserver.NewP2PServer(cfg.Listeners)
	if err != nil {
		return nil, err
	}
	rpcServer, err := grpcserver.NewRPCServer(cfg.RPCListeners, cfg.RPCMaxClients)
	if err != nil {
		return nil, err
	}
	adapter := NetAdapter{
		cfg:       cfg,
		id:        netAdapterID,
		p2pServer: p2pServer,
		rpcServer: rpcServer,

		p2pConnections:     make(map[*NetConnection]struct{}),
		outboundP2PRouters: make(map[string]*routerpkg.Router),
	}

	adapter.p2pServer.SetOnConnectedHandler(adapter.onP2PConnectedHandler)
	adapter.rpcServer.SetOnConnectedHandler(adapter.onRPCConnectedHandler)

	return &adapter, nil
}

// Start begins the operation of the NetAdapter
func (na *NetAdapter) Start() error {
	if na.p2pRouterInitializer == nil {
		return errors.New("p2pRouterInitializer was not set")
	}
	if na.rpcRouterInitializer == nil {
		return errors.New("rpcRouterInitializer was not set")
	}

	err := na.p2pServer.Start()
	if err != nil {
		return err
	}
	err = na.rpcServer.Start()
	if err != nil {
		return err
	}

	return nil
}

// Stop safely closes the NetAdapter
func (na *NetAdapter) Stop() error {
	if na.stop.Add(1) != 1 {
		return errors.New("net adapter stopped more than once")
	}
	err := na.p2pServer.Stop()
	if err != nil {
		return err
	}
	return na.rpcServer.Stop()
}

// P2PConnect tells the NetAdapter's underlying p2p server to initiate a connection
// to the given address
func (na *NetAdapter) P2PConnect(address string) error {
	if na.cfg != nil && na.cfg.DisallowLoopbackP2PConnections {
		if host, _, err := net.SplitHostPort(address); err == nil {
			if strings.EqualFold(host, "localhost") {
				return errors.Errorf("refusing to P2P connect to loopback address %q", address)
			}
			if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
				return errors.Errorf("refusing to P2P connect to loopback address %q", address)
			}
		}
	}
	_, err := na.p2pServer.Connect(address)
	return err
}

// P2PConnections returns a list of p2p connections currently connected and active
func (na *NetAdapter) P2PConnections() []*NetConnection {
	na.p2pConnectionsLock.RLock()
	defer na.p2pConnectionsLock.RUnlock()

	netConnections := make([]*NetConnection, 0, len(na.p2pConnections))

	for netConnection := range na.p2pConnections {
		netConnections = append(netConnections, netConnection)
	}

	return netConnections
}

// P2PConnectionCount returns the count of the connected p2p connections
func (na *NetAdapter) P2PConnectionCount() int {
	na.p2pConnectionsLock.RLock()
	defer na.p2pConnectionsLock.RUnlock()

	return len(na.p2pConnections)
}

func (na *NetAdapter) onP2PConnectedHandler(connection server.Connection) error {
	peerAddress := connection.Address().String()
	routerName := fmt.Sprintf("P2P %s", peerAddress)

	var routerForConnection *routerpkg.Router
	if connection.IsOutbound() {
		na.outboundP2PRoutersLock.Lock()
		routerForConnection = na.outboundP2PRouters[peerAddress]
		if routerForConnection == nil {
			routerForConnection = routerpkg.NewRouter(routerName)
			na.outboundP2PRouters[peerAddress] = routerForConnection
		} else {
			routerForConnection.Reset(routerName)
		}
		na.outboundP2PRoutersLock.Unlock()
	} else {
		routerForConnection = routerpkg.NewRouter(routerName)
	}

	// forgetOutboundRouter drops this connection's router from the outbound cache, unless a newer
	// connection to the same address has already replaced it there.
	forgetOutboundRouter := func() {
		if !connection.IsOutbound() {
			return
		}
		na.outboundP2PRoutersLock.Lock()
		defer na.outboundP2PRoutersLock.Unlock()
		if na.outboundP2PRouters[peerAddress] == routerForConnection {
			delete(na.outboundP2PRouters, peerAddress)
			log.Debugf("Removed cached outbound router for peer: %s", peerAddress)
		}
	}

	netConnection := newNetConnection(connection, na.p2pRouterInitializer, routerName, routerForConnection)
	if netConnection.ErrorMessage != nil {
		forgetOutboundRouter()
		return nil // don't do anything further since handshake failed.
	}

	na.p2pConnectionsLock.Lock()
	defer na.p2pConnectionsLock.Unlock()

	netConnection.setOnDisconnectedHandler(func() {
		// 1. Remove from the active connections map
		na.p2pConnectionsLock.Lock()
		delete(na.p2pConnections, netConnection)
		na.p2pConnectionsLock.Unlock()

		// 2. Immediately purge the router from the outbound cache if applicable
		forgetOutboundRouter()
	})

	na.p2pConnections[netConnection] = struct{}{}

	// The router initializer runs asynchronous checks (the ban check among them) that can disconnect
	// the peer before the handler above was installed. The gRPC connection fires its disconnected
	// handler only once, so that disconnect is not reported again, and starting an already
	// disconnected connection does nothing - the entry and the cached router used to stay forever.
	if !connection.IsConnected() {
		delete(na.p2pConnections, netConnection)
		forgetOutboundRouter()
		return nil
	}

	netConnection.start()

	return nil
}

func (na *NetAdapter) onRPCConnectedHandler(connection server.Connection) error {
	netConnection := newNetConnection(connection, na.rpcRouterInitializer, "on RPC connected", nil)
	if netConnection.ErrorMessage != nil {
		return nil // don't do anything further since handshake failed.
	}
	// rpcRouterInitializer (a different package) runs inside newNetConnection above and may already
	// have registered its own disconnect handler via the exported SetOnDisconnectedHandler - e.g. to
	// remove this connection's RPC notification listener the instant the connection is known dead,
	// rather than waiting for its message-handling loop to notice (which can be blocked for a long
	// time on an unrelated slow request). Only fall back to a no-op here if it didn't, since start()
	// requires some handler to be set.
	if netConnection.onDisconnectedHandler == nil {
		netConnection.setOnDisconnectedHandler(func() {})
	}
	netConnection.start()

	return nil
}

// SetP2PRouterInitializer sets the p2pRouterInitializer function
// for the net adapter
func (na *NetAdapter) SetP2PRouterInitializer(routerInitializer RouterInitializer) {
	na.p2pRouterInitializer = routerInitializer
}

// SetRPCRouterInitializer sets the rpcRouterInitializer function
// for the net adapter
func (na *NetAdapter) SetRPCRouterInitializer(routerInitializer RouterInitializer) {
	na.rpcRouterInitializer = routerInitializer
}

// ID returns this netAdapter's ID in the network
func (na *NetAdapter) ID() *id.ID {
	return na.id
}

// P2PBroadcast sends the given `message` to every peer corresponding
// to each NetConnection in the given netConnections
func (na *NetAdapter) P2PBroadcast(netConnections []*NetConnection, message appmessage.Message) error {
	na.p2pConnectionsLock.RLock()
	defer na.p2pConnectionsLock.RUnlock()

	for _, netConnection := range netConnections {
		err := netConnection.router.OutgoingRoute().Enqueue(message)
		if err != nil {
			if errors.Is(err, routerpkg.ErrRouteClosed) {
				log.Debugf("Cannot enqueue message to %s: router is closed", netConnection)
				continue
			}
			// A peer too slow to drain its outgoing route only misses this message. Returning here
			// used to stop the broadcast for every peer after it, and to fail the flow that
			// broadcast - disconnecting that flow's own peer rather than the saturated one.
			if errors.Is(err, routerpkg.ErrRouteCapacityReached) {
				log.Debugf("Cannot enqueue message to %s: outgoing route is full", netConnection)
				continue
			}
			return err
		}
	}
	return nil
}
