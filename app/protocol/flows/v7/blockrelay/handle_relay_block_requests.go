package blockrelay

import (
	"runtime"
	"sync/atomic"
	"time"

	"github.com/Hoosat-Oy/HTND/app/appmessage"
	peerpkg "github.com/Hoosat-Oy/HTND/app/protocol/peer"
	"github.com/Hoosat-Oy/HTND/domain"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/infrastructure/network/netadapter/router"
)

// RelayBlockRequestsContext is the interface for the context needed for the HandleRelayBlockRequests flow.
type RelayBlockRequestsContext interface {
	Domain() domain.Domain
}

const getBlockRetryInterval = 10 * time.Millisecond

// HandleRelayBlockRequests listens to appmessage.MsgRequestRelayBlocks messages and sends
// their corresponding blocks to the requesting peer.
func HandleRelayBlockRequests(context RelayBlockRequestsContext, incomingRoute *router.Route,
	outgoingRoute *router.Route, peer *peerpkg.Peer,
) error {
	threadcount := runtime.NumCPU() * 2
	semaphore := make(chan struct{}, threadcount)

	rateLimit := time.NewTicker(time.Second / time.Duration(threadcount*2))
	defer rateLimit.Stop()

	for {
		<-rateLimit.C // wait for rate limiter
		var done atomic.Bool
		message, err := incomingRoute.Dequeue()
		if err != nil {
			return err
		}
		getRelayBlocksMessage := message.(*appmessage.MsgRequestRelayBlocks)
		hashesLen := len(getRelayBlocksMessage.Hashes)
		for i := range hashesLen {
			if done.Load() {
				return nil
			}
			hash := getRelayBlocksMessage.Hashes[i]
			semaphore <- struct{}{} // acquire
			go func(hash *externalapi.DomainHash) {
				defer func() { <-semaphore }() // release
				if done.Load() {
					return
				}
				block, found, err := context.Domain().Consensus().GetBlock(hash)
				if err != nil {
					log.Warnf("unable to fetch requested block hash %s: %s", hash, err)
					done.Store(true)
					return
				}
				if !found {
					log.Warnf("Relay block %s not found", hash)
					done.Store(true)
					return
				}

				log.Debugf("Relaying block %s to peer %s", hash, peer.Address())
				err = outgoingRoute.Enqueue(appmessage.DomainBlockToMsgBlock(block))
				if err != nil {
					log.Warnf("failed to enqueue block %s: %s", hash, err)
					done.Store(true)
					return
				}
			}(hash)
		}
	}
}
