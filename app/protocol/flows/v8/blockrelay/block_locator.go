package blockrelay

import (
	"time"

	"github.com/HoosatNetwork/HTND/app/appmessage"
	"github.com/HoosatNetwork/HTND/app/protocol/common"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/infrastructure/network/netadapter/router"
	"github.com/pkg/errors"
)

func (flow *handleRelayInvsFlow) sendGetBlockLocator(highHash *externalapi.DomainHash, limit uint32) error {
	msgGetBlockLocator := appmessage.NewMsgRequestBlockLocator(highHash, limit)
	return flow.outgoingRoute.Enqueue(msgGetBlockLocator)
}

func (flow *handleRelayInvsFlow) receiveBlockLocator() (blockLocatorHashes []*externalapi.DomainHash, err error) {
	timer := time.NewTimer(common.DefaultTimeout)
	defer timer.Stop()

	const maxInvQueueLen = 5000
	for {
		select {
		case <-timer.C:
			return nil, errors.Wrapf(router.ErrTimeout, "timed out waiting for block locator")
		case <-flow.incomingDone:
			return nil, flow.getIncomingErr()
		case inv, ok := <-flow.invChan:
			if !ok {
				return nil, flow.getIncomingErr()
			}
			if len(flow.invsQueue) < maxInvQueueLen {
				flow.invsQueue = append(flow.invsQueue, inv)
			}
		case locator, ok := <-flow.locatorChan:
			if !ok {
				return nil, flow.getIncomingErr()
			}
			return locator.BlockLocatorHashes, nil
		}
	}
}
