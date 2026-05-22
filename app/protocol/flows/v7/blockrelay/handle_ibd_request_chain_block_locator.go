package blockrelay

import (
	"github.com/Hoosat-Oy/HTND/app/appmessage"
	"github.com/Hoosat-Oy/HTND/domain"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/infrastructure/network/netadapter/router"
)

// RequestIBDChainBlockLocatorContext is the interface for the context needed for the HandleRequestBlockLocator flow.
type RequestIBDChainBlockLocatorContext interface {
	Domain() domain.Domain
}

type handleRequestIBDChainBlockLocatorFlow struct {
	RequestIBDChainBlockLocatorContext
	incomingRoute, outgoingRoute *router.Route
}

// HandleRequestIBDChainBlockLocator handles getBlockLocator messages
func HandleRequestIBDChainBlockLocator(context RequestIBDChainBlockLocatorContext, incomingRoute *router.Route,
	outgoingRoute *router.Route,
) error {
	flow := &handleRequestIBDChainBlockLocatorFlow{
		RequestIBDChainBlockLocatorContext: context,
		incomingRoute:                      incomingRoute,
		outgoingRoute:                      outgoingRoute,
	}
	return flow.start()
}

func (flow *handleRequestIBDChainBlockLocatorFlow) start() error {
	for {
		highHash, lowHash, err := flow.receiveRequestIBDChainBlockLocator()
		if err != nil {
			return err
		}
		log.Debugf("Received getIBDChainBlockLocator with highHash: %s, lowHash: %s", highHash, lowHash)

		var locator externalapi.BlockLocator
		if highHash == nil || lowHash == nil {
			locator, err = flow.Domain().Consensus().CreateFullHeadersSelectedChainBlockLocator()
		} else {
			locator, err = flow.Domain().Consensus().CreateHeadersSelectedChainBlockLocator(lowHash, highHash)
		}

		if err != nil {
			log.Infof("Couldn't build a block locator between %s and %s", lowHash, highHash)
			log.Infof("Error: %s", err.Error())
			// The chain has been modified, signal it by sending an empty locator
			locator, err = externalapi.BlockLocator{}, nil
		}

		err = flow.sendIBDChainBlockLocator(locator)
		if err != nil {
			return err
		}
	}
}

func (flow *handleRequestIBDChainBlockLocatorFlow) receiveRequestIBDChainBlockLocator() (highHash, lowHash *externalapi.DomainHash, err error) {
	message, err := flow.incomingRoute.Dequeue()
	if err != nil {
		return nil, nil, err
	}
	msgGetBlockLocator := message.(*appmessage.MsgRequestIBDChainBlockLocator)

	return msgGetBlockLocator.HighHash, msgGetBlockLocator.LowHash, nil
}

func (flow *handleRequestIBDChainBlockLocatorFlow) sendIBDChainBlockLocator(locator externalapi.BlockLocator) error {
	msgIBDChainBlockLocator := appmessage.NewMsgIBDChainBlockLocator(locator)
	err := flow.outgoingRoute.Enqueue(msgIBDChainBlockLocator)
	if err != nil {
		return err
	}
	return nil
}
