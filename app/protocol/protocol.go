package protocol

import (
	"sync"
	"sync/atomic"

	"github.com/HoosatNetwork/HTND/v2/app/protocol/common"
	"github.com/HoosatNetwork/HTND/v2/app/protocol/flows/ready"
	v8 "github.com/HoosatNetwork/HTND/v2/app/protocol/flows/v8"

	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/HoosatNetwork/HTND/v2/app/protocol/flows/handshake"
	peerpkg "github.com/HoosatNetwork/HTND/v2/app/protocol/peer"
	"github.com/HoosatNetwork/HTND/v2/app/protocol/protocolerrors"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/addressmanager"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/connmanager"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter"
	routerpkg "github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/router"
	"github.com/pkg/errors"
)

func (m *Manager) routerInitializer(router *routerpkg.Router, netConnection *netadapter.NetConnection) {
	// isStopping flag is raised the moment that the connection associated with this router is disconnected
	// errChan is used by the flow goroutines to return to runFlows when an error occurs.
	// They are both initialized here and passed to register flows.
	isStopping := uint32(0)
	errChan := make(chan error, 100)

	receiveVersionRoute, sendVersionRoute, receiveReadyRoute := registerHandshakeRoutes(router)

	// A connection whose router is initialized just before the net adapter stops can get here after Close. That is
	// part of shutdown, not a programming error, and the net adapter has already disconnected the connection (it is
	// stopped before Close is called).
	//
	// The router is added to routersWaitGroup here, before its goroutine is spawned, so that Close waits for it. Added
	// from inside the goroutine, it could be added after Close's Wait had found no routers and returned, or while
	// that Wait was being woken by the last router to finish, which the WaitGroup does not allow.
	if !m.addRouter() {
		log.Debugf("Not running flows for %s: the protocol manager is closed", netConnection)
		return
	}

	// After flows were registered - spawn a new thread that will wait for connection to finish initializing
	// and start receiving messages
	spawn("routerInitializer-runFlows", func() {
		defer m.routersWaitGroup.Done()

		if m.isClosed.Load() == 1 {
			log.Debugf("Not running flows for %s: the protocol manager is closed", netConnection)
			return
		}
		log.Debugf("initializing route for %s", netConnection)
		isBanned, err := m.context.ConnectionManager().IsBanned(netConnection)
		if err != nil && !errors.Is(err, addressmanager.ErrAddressNotFound) {
			m.handleError(err, netConnection, router.OutgoingRoute())
			return
		}
		if isBanned {
			log.Infof("Peer %s is banned. Disconnecting...", netConnection)
			netConnection.Disconnect()
			return
		}

		netConnection.SetOnInvalidMessageHandler(func(err error) {
			if atomic.AddUint32(&isStopping, 1) == 1 {
				errChan <- protocolerrors.Wrap(true, err, "received bad message")
			}
		})
		log.Debugf("Doing Handshake with %s", netConnection)
		peer, err := handshake.HandleHandshake(m.context, netConnection, receiveVersionRoute, sendVersionRoute, router.OutgoingRoute())
		if err != nil {
			// non-blocking read from channel
			select {
			case innerError := <-errChan:
				if errors.Is(err, routerpkg.ErrRouteClosed) {
					m.handleError(innerError, netConnection, router.OutgoingRoute())
				} else {
					log.Errorf("Peer %s sent invalid message: %s", netConnection, innerError)
					m.handleError(err, netConnection, router.OutgoingRoute())
				}
			default:
				m.handleError(err, netConnection, router.OutgoingRoute())
			}
			netConnection.ErrorMessage = err
			return
		}
		defer m.context.RemoveFromPeers(peer)

		var flows []*common.Flow
		log.Debugf("Registering p2p flows for peer %s for protocol version %d", peer, peer.ProtocolVersion())
		flows, err = registerFlowsForProtocol(m, netConnection, router, errChan, &isStopping, peer.ProtocolVersion())
		if err != nil {
			log.Warnf("Disconnecting peer %s with unsupported protocol version %d", peer, peer.ProtocolVersion())
			m.handleError(err, netConnection, router.OutgoingRoute())
			return
		}
		log.Debugf("Registered p2p flows for peer %s.", peer)

		err = ready.HandleReady(receiveReadyRoute, router.OutgoingRoute(), peer)
		if err != nil {
			m.handleError(err, netConnection, router.OutgoingRoute())
			return
		}
		log.Debugf("HandleReady for peer %s", peer)
		removeHandshakeRoutes(router)

		log.Debugf("Registered p2p flows and running them for peer %s", peer)
		m.runFlowsUntilStopped(flows, peer, errChan, router, netConnection)
	})
}

// runFlowsUntilStopped runs the peer's flows until the first of them stops, then closes the router and waits for the
// rest. The peer leaves the peers list as soon as the router is closed, not once every flow has returned: some flows
// notice only later (SendPings on its next tick, up to two minutes on), and while the entry is there the same peer
// cannot connect again.
func (m *Manager) runFlowsUntilStopped(flows []*common.Flow, peer *peerpkg.Peer, errChan <-chan error,
	router *routerpkg.Router, netConnection *netadapter.NetConnection,
) {
	flowsWaitGroup := &sync.WaitGroup{}
	err := m.runFlows(flows, peer, errChan, flowsWaitGroup)
	router.Close()
	m.context.RemoveFromPeers(peer)
	if err != nil {
		m.handleError(err, netConnection, router.OutgoingRoute())
	}
	flowsWaitGroup.Wait()
}

func registerFlowsForProtocol(m *Manager, netConnection *netadapter.NetConnection, router *routerpkg.Router,
	errChan chan error, isStopping *uint32, protocolVersion uint32,
) ([]*common.Flow, error) {
	switch protocolVersion {
	case 8, 11: // Foztor 5th Oct 2027 - really they are the same thing, just a HF enforcement
		return v8.Register(m, netConnection, router, errChan, isStopping), nil
	default:
		return nil, protocolerrors.Errorf(false, "peer protocol version %d is not accepted", protocolVersion)
	}
}

func (m *Manager) handleError(err error, netConnection *netadapter.NetConnection, outgoingRoute *routerpkg.Route) {
	netConnection.ErrorMessage = err
	if protocolErr := (protocolerrors.ProtocolError{}); errors.As(err, &protocolErr) {
		if protocolErr.ShouldBan && (m.context.Config().EnableBanning || protocolErr.ForceBan) {
			log.Warnf("Banning %s (reason: %s)", netConnection, protocolErr.Cause)
			err := m.context.ConnectionManager().Ban(netConnection)
			if err != nil && !errors.Is(err, connmanager.ErrCannotBanPermanent) {
				return
			}
			err = outgoingRoute.Enqueue(appmessage.NewMsgReject(protocolErr.Error()))
			if err != nil && !errors.Is(err, routerpkg.ErrRouteClosed) {
				return
			}
		}
		log.Debugf("Disconnecting from %s (reason: %s)", netConnection, protocolErr.Cause)
		netConnection.Disconnect()
		return
	}

	if errors.Is(err, common.ErrHandshakeTimeout) {
		log.Debugf("Handshake timed out for connection %v. Disconnecting..", netConnection)
		netConnection.Disconnect()
		return
	}

	if errors.Is(err, common.ErrPeerWithSameIDExists) {
		log.Debugf("Peer already exists %v. Disconnecting..", netConnection)
		netConnection.Disconnect()
		return
	}
	if errors.Is(err, routerpkg.ErrTimeout) {
		log.Debugf("Got timeout from %s. Disconnecting...", netConnection)
		netConnection.Disconnect()
		return
	}
	if errors.Is(err, routerpkg.ErrRouteClosed) {
		log.Debugf("Route closed: %v", netConnection)
		return
	}
	if errors.Is(err, addressmanager.ErrAddressNotFound) {
		log.Debugf("Address not found: %v", netConnection)
		return
	}
	panic(err)
}

// RegisterFlow registers a flow to the given router.
func (m *Manager) RegisterFlow(name string, router *routerpkg.Router, messageTypes []appmessage.MessageCommand, isStopping *uint32,
	errChan chan error, initializeFunc common.FlowInitializeFunc,
) *common.Flow {
	route, err := router.AddIncomingRoute(name, messageTypes)
	if err != nil {
		panic(err)
	}

	return m.registerFlowForRoute(route, name, isStopping, errChan, initializeFunc)
}

// RegisterFlowWithCapacity registers a flow to the given router with a custom capacity.
func (m *Manager) RegisterFlowWithCapacity(name string, capacity int, router *routerpkg.Router,
	messageTypes []appmessage.MessageCommand, isStopping *uint32,
	errChan chan error, initializeFunc common.FlowInitializeFunc,
) *common.Flow {
	route, err := router.AddIncomingRouteWithCapacity(name, capacity, messageTypes)
	if err != nil {
		panic(err)
	}

	return m.registerFlowForRoute(route, name, isStopping, errChan, initializeFunc)
}

func (m *Manager) registerFlowForRoute(route *routerpkg.Route, name string, isStopping *uint32,
	errChan chan error, initializeFunc common.FlowInitializeFunc,
) *common.Flow {
	return &common.Flow{
		Name: name,
		ExecuteFunc: func(peer *peerpkg.Peer) {
			err := initializeFunc(route, peer)
			if err != nil {
				m.context.HandleError(err, name, isStopping, errChan)
				return
			}
		},
	}
}

// RegisterOneTimeFlow registers a one-time flow (that exits once some operations are done) to the given router.
func (m *Manager) RegisterOneTimeFlow(name string, router *routerpkg.Router, messageTypes []appmessage.MessageCommand,
	isStopping *uint32, stopChan chan error, initializeFunc common.FlowInitializeFunc,
) *common.Flow {
	route, err := router.AddIncomingRoute(name, messageTypes)
	if err != nil {
		panic(err)
	}

	return &common.Flow{
		Name: name,
		ExecuteFunc: func(peer *peerpkg.Peer) {
			defer func() {
				if len(messageTypes) == 0 {
					router.ReleaseRoute(route)
					return
				}

				err := router.RemoveRoute(messageTypes)
				if err != nil {
					panic(err)
				}
			}()

			err := initializeFunc(route, peer)
			if err != nil {
				m.context.HandleError(err, name, isStopping, stopChan)
				return
			}
		},
	}
}

func registerHandshakeRoutes(router *routerpkg.Router) (
	receiveVersionRoute, sendVersionRoute, receiveReadyRoute *routerpkg.Route,
) {
	receiveVersionRoute, err := router.AddIncomingRoute("recieveVersion - incoming", []appmessage.MessageCommand{appmessage.CmdVersion})
	if err != nil {
		panic(err)
	}

	sendVersionRoute, err = router.AddIncomingRoute("sendVersion - incoming", []appmessage.MessageCommand{appmessage.CmdVerAck})
	if err != nil {
		panic(err)
	}

	receiveReadyRoute, err = router.AddIncomingRoute("recieveReady - incoming", []appmessage.MessageCommand{appmessage.CmdReady})
	if err != nil {
		panic(err)
	}

	return receiveVersionRoute, sendVersionRoute, receiveReadyRoute
}

func removeHandshakeRoutes(router *routerpkg.Router) {
	err := router.RemoveRoute([]appmessage.MessageCommand{appmessage.CmdVersion, appmessage.CmdVerAck, appmessage.CmdReady})
	if err != nil {
		panic(err)
	}
}
