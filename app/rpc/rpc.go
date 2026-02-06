package rpc

import (
	"github.com/Hoosat-Oy/HTND/app/appmessage"
	"github.com/Hoosat-Oy/HTND/app/rpc/rpccontext"
	"github.com/Hoosat-Oy/HTND/app/rpc/rpchandlers"
	"github.com/Hoosat-Oy/HTND/app/rpc/rpcstats"
	"github.com/Hoosat-Oy/HTND/infrastructure/network/netadapter"
	"github.com/Hoosat-Oy/HTND/infrastructure/network/netadapter/router"
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
	appmessage.CmdNotifyVirtualSelectedParentChainChangedRequestMessage:     rpchandlers.HandleNotifyVirtualSelectedParentChainChanged,
	appmessage.CmdGetBlockRequestMessage:                                    rpchandlers.HandleGetBlock,
	appmessage.CmdGetBlockByTransactionIDRequestMessage:                     rpchandlers.HandleGetBlockByTransactionID,
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
}

func (m *Manager) routerInitializer(router *router.Router, netConnection *netadapter.NetConnection) {
	messageTypes := make([]appmessage.MessageCommand, 0, len(handlers))
	for messageType := range handlers {
		messageTypes = append(messageTypes, messageType)
	}
	incomingRoute, err := router.AddIncomingRoute("rpc router", messageTypes)
	if err != nil {
		panic(err)
	}
	m.context.NotificationManager.AddListener(router)

	spawn("routerInitializer-handleIncomingMessages", func() {
		defer m.context.NotificationManager.RemoveListener(router)

		err := m.handleIncomingMessages(router, incomingRoute, netConnection)
		m.handleError(err, netConnection)
	})
}

func (m *Manager) handleIncomingMessages(router *router.Router, incomingRoute *router.Route, netConnection *netadapter.NetConnection) error {
	clientAddress := netConnection.Address()
	outgoingRoute := router.OutgoingRoute()
	for {
		request, err := incomingRoute.Dequeue()
		if err != nil {
			return err
		}
		handler, ok := handlers[request.Command()]
		if !ok {
			return err
		}

		// Record the RPC request for statistics
		RPCStats.RecordRequest(clientAddress, string(request.Command()))

		response, err := handler(m.context, router, request)
		if err != nil {
			return err
		}
		err = outgoingRoute.Enqueue(response)
		if err != nil {
			return err
		}
	}
}

func (m *Manager) handleError(err error, netConnection *netadapter.NetConnection) {
	if errors.Is(err, router.ErrTimeout) {
		log.Warnf("Got timeout from %s. Disconnecting...", netConnection)
		netConnection.Disconnect()
		return
	}
	if errors.Is(err, router.ErrRouteClosed) {
		return
	}
	panic(err)
}
