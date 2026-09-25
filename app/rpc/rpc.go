package rpc

import (
	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/HoosatNetwork/HTND/v2/app/rpc/rpccontext"
	"github.com/HoosatNetwork/HTND/v2/app/rpc/rpchandlers"
	"github.com/HoosatNetwork/HTND/v2/app/rpc/rpcstats"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/router"
	"github.com/pkg/errors"
)

// RPCStats is the global RPC statistics tracker
var RPCStats = rpcstats.NewStats()

type handler func(context *rpccontext.Context, router *router.Router, request appmessage.Message) (appmessage.Message, error)

var handlers = map[appmessage.MessageCommand]handler{
	appmessage.CmdGetCurrentNetworkRequestMessage:                           rpchandlers.HandleGetCurrentNetwork,
	appmessage.CmdSubmitBlockRequestMessage:                                 rpchandlers.HandleSubmitBlock,
	appmessage.CmdGetBlockTemplateRequestMessage:                            rpchandlers.HandleGetBlockTemplate,
	appmessage.CmdNotifyBlockAddedRequestMessage:                            rpchandlers.HandleNotifyBlockAdded,
	appmessage.CmdGetPeerAddressesRequestMessage:                            rpchandlers.HandleGetPeerAddresses,
	appmessage.CmdGetSelectedTipHashRequestMessage:                          rpchandlers.HandleGetSelectedTipHash,
	appmessage.CmdGetMempoolEntryRequestMessage:                             rpchandlers.HandleGetMempoolEntry,
	appmessage.CmdGetConnectedPeerInfoRequestMessage:                        rpchandlers.HandleGetConnectedPeerInfo,
	appmessage.CmdAddPeerRequestMessage:                                     rpchandlers.HandleAddPeer,
	appmessage.CmdSubmitTransactionRequestMessage:                           rpchandlers.HandleSubmitTransaction,
	appmessage.CmdSubmitTransactionReplacementRequestMessage:                rpchandlers.HandleSubmitTransactionReplacement,
	appmessage.CmdGetFeeEstimateRequestMessage:                              rpchandlers.HandleGetFeeEstimate,
	appmessage.CmdNotifyVirtualSelectedParentChainChangedRequestMessage:     rpchandlers.HandleNotifyVirtualSelectedParentChainChanged,
	appmessage.CmdGetBlockRequestMessage:                                    rpchandlers.HandleGetBlock,
	appmessage.CmdGetBlockByTransactionIDRequestMessage:                     rpchandlers.HandleGetBlockByTransactionID,
	appmessage.CmdGetTransactionStatusRequestMessage:                        rpchandlers.HandleGetTransactionStatus,
	appmessage.CmdGetSubnetworkRequestMessage:                               rpchandlers.HandleGetSubnetwork,
	appmessage.CmdGetVirtualSelectedParentChainFromBlockRequestMessage:      rpchandlers.HandleGetVirtualSelectedParentChainFromBlock,
	appmessage.CmdGetBlocksRequestMessage:                                   rpchandlers.HandleGetBlocks,
	appmessage.CmdGetBlockCountRequestMessage:                               rpchandlers.HandleGetBlockCount,
	appmessage.CmdGetBalanceByAddressRequestMessage:                         rpchandlers.HandleGetBalanceByAddress,
	appmessage.CmdGetBlockDAGInfoRequestMessage:                             rpchandlers.HandleGetBlockDAGInfo,
	appmessage.CmdResolveFinalityConflictRequestMessage:                     rpchandlers.HandleResolveFinalityConflict,
	appmessage.CmdNotifyFinalityConflictsRequestMessage:                     rpchandlers.HandleNotifyFinalityConflicts,
	appmessage.CmdGetMempoolEntriesRequestMessage:                           rpchandlers.HandleGetMempoolEntries,
	appmessage.CmdShutDownRequestMessage:                                    rpchandlers.HandleShutDown,
	appmessage.CmdGetHeadersRequestMessage:                                  rpchandlers.HandleGetHeaders,
	appmessage.CmdNotifyUTXOsChangedRequestMessage:                          rpchandlers.HandleNotifyUTXOsChanged,
	appmessage.CmdStopNotifyingUTXOsChangedRequestMessage:                   rpchandlers.HandleStopNotifyingUTXOsChanged,
	appmessage.CmdGetUTXOsByAddressesRequestMessage:                         rpchandlers.HandleGetUTXOsByAddresses,
	appmessage.CmdGetBalancesByAddressesRequestMessage:                      rpchandlers.HandleGetBalancesByAddresses,
	appmessage.CmdGetVirtualSelectedParentBlueScoreRequestMessage:           rpchandlers.HandleGetVirtualSelectedParentBlueScore,
	appmessage.CmdNotifyVirtualSelectedParentBlueScoreChangedRequestMessage: rpchandlers.HandleNotifyVirtualSelectedParentBlueScoreChanged,
	appmessage.CmdBanRequestMessage:                                         rpchandlers.HandleBan,
	appmessage.CmdUnbanRequestMessage:                                       rpchandlers.HandleUnban,
	appmessage.CmdGetInfoRequestMessage:                                     rpchandlers.HandleGetInfo,
	appmessage.CmdNotifyPruningPointUTXOSetOverrideRequestMessage:           rpchandlers.HandleNotifyPruningPointUTXOSetOverrideRequest,
	appmessage.CmdStopNotifyingPruningPointUTXOSetOverrideRequestMessage:    rpchandlers.HandleStopNotifyingPruningPointUTXOSetOverrideRequest,
	appmessage.CmdEstimateNetworkHashesPerSecondRequestMessage:              rpchandlers.HandleEstimateNetworkHashesPerSecond,
	appmessage.CmdNotifyVirtualDaaScoreChangedRequestMessage:                rpchandlers.HandleNotifyVirtualDaaScoreChanged,
	appmessage.CmdNotifyNewBlockTemplateRequestMessage:                      rpchandlers.HandleNotifyNewBlockTemplate,
	appmessage.CmdGetCoinSupplyRequestMessage:                               rpchandlers.HandleGetCoinSupply,
	appmessage.CmdGetMempoolEntriesByAddressesRequestMessage:                rpchandlers.HandleGetMempoolEntriesByAddresses,
	appmessage.CmdGetUsableAddressesRequestMessage:                          rpchandlers.HandleGetUsableAddresses,
	appmessage.CmdGetPaginatedUTXOsByAddressesRequestMessage:                rpchandlers.HandleGetPaginatedUTXOsByAddresses,
}

func (m *Manager) routerInitializer(rtr *router.Router, netConnection *netadapter.NetConnection) {
	messageTypes := make([]appmessage.MessageCommand, 0, len(handlers))
	for messageType := range handlers {
		messageTypes = append(messageTypes, messageType)
	}
	rtr.OutgoingRoute().SetOnCapacityReachedHandler(func(route *router.Route, message appmessage.Message) {
		log.Warnf("Disconnecting slow RPC client %s because outgoing route '%s' is full (%d/%d) while sending '%s'",
			netConnection, route.Name(), route.Length(), route.Capacity(), message.Command())
		netConnection.Disconnect()
	})
	incomingRoute, err := rtr.AddIncomingRoute("rpc router", messageTypes)
	if err != nil {
		panic(err)
	}
	m.context.NotificationManager.AddListener(rtr)

	// Removing the listener also happens below, deferred until handleIncomingMessages returns - but
	// that loop notices a dead connection only the next time it calls incomingRoute.Dequeue(), and it
	// can be blocked well past that point pushing an address-index request (GetUsableAddressesRequest
	// and friends) into a full, serialized per-connection queue while its own worker is stuck behind
	// slow consensus-lock contention. A client that disconnects during that wait left its listener
	// registered indefinitely, so every future notification broadcast hit its already-closed outgoing
	// route forever - not a transient race, a permanent leak that only grows as clients reconnect.
	// The transport layer knows the connection is dead immediately (NetConnection.router.Close() runs
	// synchronously from the disconnect callback), so remove the listener from there too - whichever
	// path notices first wins, and RemoveListener on an already-removed listener is a no-op.
	netConnection.SetOnDisconnectedHandler(func() {
		m.context.NotificationManager.RemoveListener(rtr)
	})

	spawn("routerInitializer-handleIncomingMessages", func() {
		defer m.context.NotificationManager.RemoveListener(rtr)

		err := m.handleIncomingMessages(rtr, incomingRoute, netConnection.Address(), netConnection)
		m.handleError(err, netConnection)
	})
}

// addressIndexCommands are the requests that check every coin of an address against virtual's UTXO set. For an address
// with many coins, such as a mining pool's, that takes minutes, so they are handled apart from the client's other
// requests.
var addressIndexCommands = map[appmessage.MessageCommand]struct{}{
	appmessage.CmdGetBalanceByAddressRequestMessage:          {},
	appmessage.CmdGetBalancesByAddressesRequestMessage:       {},
	appmessage.CmdGetUTXOsByAddressesRequestMessage:          {},
	appmessage.CmdGetPaginatedUTXOsByAddressesRequestMessage: {},
	appmessage.CmdGetUsableAddressesRequestMessage:           {},
}

const addressIndexRequestsQueueSize = 100

func (m *Manager) handleIncomingMessages(router *router.Router, incomingRoute *router.Route, clientAddress string,
	netConnection *netadapter.NetConnection,
) error {
	// A client's requests are handled one at a time, and a pool that asked for its address's balance on the connection
	// it mines through had its GetBlockTemplate and SubmitBlock requests wait minutes behind it. Address-index requests
	// go to their own worker, still in order among themselves; responses are matched by type, not by order.
	addressIndexRequests := make(chan appmessage.Message, addressIndexRequestsQueueSize)
	defer close(addressIndexRequests)
	spawn("routerInitializer-handleAddressIndexRequests", func() {
		failed := false
		for request := range addressIndexRequests {
			// After a failure the connection is being closed; keep draining so the sender never blocks.
			if failed {
				continue
			}
			err := m.handleRequest(router, request)
			if err != nil {
				failed = true
				m.handleError(err, netConnection)
			}
		}
	})

	for {
		request, err := incomingRoute.Dequeue()
		if err != nil {
			return err
		}
		if _, ok := handlers[request.Command()]; !ok {
			return errors.Errorf("unknown RPC command %s", request.Command())
		}

		if m.context.Config != nil && m.context.Config.EnableRPCStats {
			RPCStats.RecordRequest(clientAddress, request.Command().String())
		}

		if _, ok := addressIndexCommands[request.Command()]; ok {
			addressIndexRequests <- request
			continue
		}
		err = m.handleRequest(router, request)
		if err != nil {
			return err
		}
	}
}

func (m *Manager) handleRequest(router *router.Router, request appmessage.Message) error {
	response, err := handlers[request.Command()](m.context, router, request)
	if err != nil {
		return err
	}
	return router.OutgoingRoute().Enqueue(response)
}

func (m *Manager) handleError(err error, netConnection *netadapter.NetConnection) {
	if err == nil {
		return
	}
	if errors.Is(err, router.ErrTimeout) {
		log.Warnf("Got timeout from %s. Disconnecting...", netConnection)
		netConnection.Disconnect()
		return
	}
	if errors.Is(err, router.ErrRouteClosed) {
		return
	}
	if errors.Is(err, router.ErrRouteCapacityReached) {
		log.Warnf("Disconnecting slow RPC client %s after outgoing route capacity was reached", netConnection)
		netConnection.Disconnect()
		return
	}
	// Any other error came from request handling. Treat it as a per-connection failure
	// rather than crashing the entire node.
	log.Errorf("RPC client %s disconnected due to handler error: %v", netConnection, err)
	netConnection.Disconnect()
}
