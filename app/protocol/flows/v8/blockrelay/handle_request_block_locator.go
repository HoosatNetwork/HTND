package blockrelay

import (
	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/HoosatNetwork/HTND/v2/app/protocol/protocolerrors"
	"github.com/HoosatNetwork/HTND/v2/domain"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/router"
)

// RequestBlockLocatorContext is the interface for the context needed for the HandleRequestBlockLocator flow.
type RequestBlockLocatorContext interface {
	Domain() domain.Domain
}

type handleRequestBlockLocatorFlow struct {
	RequestBlockLocatorContext
	incomingRoute, outgoingRoute *router.Route
}

// HandleRequestBlockLocator handles getBlockLocator messages
func HandleRequestBlockLocator(context RequestBlockLocatorContext, incomingRoute *router.Route,
	outgoingRoute *router.Route,
) error {
	flow := &handleRequestBlockLocatorFlow{
		RequestBlockLocatorContext: context,
		incomingRoute:              incomingRoute,
		outgoingRoute:              outgoingRoute,
	}
	return flow.start()
}

func (flow *handleRequestBlockLocatorFlow) start() error {
	for {
		highHash, limit, err := flow.receiveGetBlockLocator()
		if err != nil {
			return err
		}
		log.Debugf("Received getBlockLocator with highHash: %s, limit: %d", highHash, limit)

		locator, err := flow.Domain().Consensus().CreateBlockLocatorFromPruningPoint(highHash, limit)
		if err != nil || len(locator) == 0 {
			// A protocol error, so the peer is disconnected and retries elsewhere. This used to return
			// errors.Wrapf(err, ...), which is nil for an empty locator, ending the flow while the
			// connection stayed up and the peer waited out its 10-minute timeout. The cause is formatted
			// rather than wrapped: HandleError silently drops errors that unwrap to database not-found,
			// which would leave the flow just as dead.
			reason := "the locator is empty"
			if err != nil {
				reason = err.Error()
			}
			return protocolerrors.Errorf(false, "couldn't build a block locator between the pruning point "+
				"and %s with limit %d: %s", highHash, limit, reason)
		}

		err = flow.sendBlockLocator(locator)
		if err != nil {
			return err
		}
	}
}

func (flow *handleRequestBlockLocatorFlow) receiveGetBlockLocator() (highHash *externalapi.DomainHash, limit uint32, err error) {
	message, err := flow.incomingRoute.Dequeue()
	if err != nil {
		return nil, 0, err
	}
	msgGetBlockLocator := message.(*appmessage.MsgRequestBlockLocator)

	return msgGetBlockLocator.HighHash, msgGetBlockLocator.Limit, nil
}

func (flow *handleRequestBlockLocatorFlow) sendBlockLocator(locator externalapi.BlockLocator) error {
	msgBlockLocator := appmessage.NewMsgBlockLocator(locator)
	err := flow.outgoingRoute.Enqueue(msgBlockLocator)
	if err != nil {
		return err
	}
	return nil
}
