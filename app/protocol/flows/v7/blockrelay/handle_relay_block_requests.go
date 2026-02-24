package blockrelay

import (
	"runtime"
	"time"

	"github.com/Hoosat-Oy/HTND/app/appmessage"
	peerpkg "github.com/Hoosat-Oy/HTND/app/protocol/peer"
	"github.com/Hoosat-Oy/HTND/domain"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/pow"
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
	outgoingRoute *router.Route, peer *peerpkg.Peer) error {
	semaphore := make(chan struct{}, runtime.NumCPU())

	rateLimit := time.NewTicker(time.Second / 20)
	defer rateLimit.Stop()

	for {
		<-rateLimit.C // wait for rate limiter
		message, err := incomingRoute.Dequeue()
		if err != nil {
			return err
		}
		getRelayBlocksMessage := message.(*appmessage.MsgRequestRelayBlocks)
		hashesLen := len(getRelayBlocksMessage.Hashes)
		for i := range hashesLen {
			hash := getRelayBlocksMessage.Hashes[i]
			semaphore <- struct{}{} // acquire
			go func(hash *externalapi.DomainHash) {
				defer func() { <-semaphore }() // release
				block, found, err := context.Domain().Consensus().GetBlock(hash)
				if err != nil {
					log.Warnf("unable to fetch requested block hash %s: %s", hash, err)
					return
				}
				if !found {
					log.Warnf("Relay block %s not found", hash)
					return
				}

				if block.PoWHash == "" {
					state := pow.NewState(block.Header.ToMutable())
					_, powHash := state.CalculateProofOfWorkValue()
					block.PoWHash = powHash.String()
				}
				log.Debugf("Relaying block %s to peer %s", hash, peer.Address())
				err = outgoingRoute.Enqueue(appmessage.DomainBlockToMsgBlock(block))
				if err != nil {
					log.Warnf("failed to enqueue block %s: %s", hash, err)
					return
				}
			}(hash)
		}
	}
}
