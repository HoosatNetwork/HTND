package handshake

import (
	"time"

	"github.com/Hoosat-Oy/HTND/app/appmessage"
	peerpkg "github.com/Hoosat-Oy/HTND/app/protocol/peer"
	"github.com/Hoosat-Oy/HTND/infrastructure/logger"
	"github.com/Hoosat-Oy/HTND/infrastructure/network/netadapter/router"
	"github.com/Hoosat-Oy/HTND/version"
	"github.com/pkg/errors"
)

var (
	// userAgentName is the user agent name and is used to help identify
	// ourselves to other hoosat peers.
	userAgentName = "htnd"

	// userAgentVersion is the user agent version and is used to help
	// identify ourselves to other hoosat peers.
	userAgentVersion = version.Version()

	// defaultServices describes the default services that are supported by
	// the server.
	defaultServices = appmessage.DefaultServices
)

type sendVersionFlow struct {
	HandleHandshakeContext

	incomingRoute, outgoingRoute *router.Route
	peer                         *peerpkg.Peer
}

// SendVersion sends a version to a peer and waits for verack.
func SendVersion(context HandleHandshakeContext, incomingRoute *router.Route,
	outgoingRoute *router.Route, peer *peerpkg.Peer,
) error {
	flow := &sendVersionFlow{
		HandleHandshakeContext: context,
		incomingRoute:          incomingRoute,
		outgoingRoute:          outgoingRoute,
		peer:                   peer,
	}
	return flow.start()
}

func (flow *sendVersionFlow) start() error {
	onEnd := logger.LogAndMeasureExecutionTime(log, "sendVersionFlow.start")
	defer onEnd()

	log.Debugf("Starting sendVersionFlow with %s", flow.peer.Address())

	// Version message.
	localAddress := flow.AddressManager().BestLocalAddress(flow.peer.Connection().NetAddress())
	subnetworkID := flow.Config().SubnetworkID
	if flow.Config().ProtocolVersion < minAcceptableProtocolVersion {
		return errors.Errorf("configured protocol version %d is obsolete", flow.Config().ProtocolVersion)
	}
	msg := appmessage.NewMsgVersion(localAddress, flow.NetAdapter().ID(),
		flow.Config().ActiveNetParams.Name, subnetworkID, flow.Config().ProtocolVersion)
	msg.AddUserAgent(userAgentName, userAgentVersion, flow.Config().UserAgentComments...)

	// Advertise the services flag
	msg.Services = defaultServices

	// Advertise our max supported protocol version.
	msg.ProtocolVersion = flow.Config().ProtocolVersion

	// Advertise if inv messages for transactions are desired.
	msg.DisableRelayTx = flow.Config().BlocksOnly

	err := flow.outgoingRoute.Enqueue(msg)
	if err != nil {
		return err
	}

	// Wait for verack
	log.Debugf("Waiting for verack")
	message, err := flow.incomingRoute.DequeueWithTimeout(60 * time.Second)
	if err != nil {
		log.Debugf("Verack errored %s", message)
		return err
	}
	log.Debugf("Got verack")
	return nil
}
