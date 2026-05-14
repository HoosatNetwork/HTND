package blockrelay

import (
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Hoosat-Oy/HTND/app/appmessage"
	peerpkg "github.com/Hoosat-Oy/HTND/app/protocol/peer"
	"github.com/Hoosat-Oy/HTND/domain"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/infrastructure/network/netadapter/router"
)

// HandleIBDBlockRequestsContext is the interface for the context needed for the HandleIBDBlockRequests flow.
type HandleIBDBlockRequestsContext interface {
	Domain() domain.Domain
}

// HandleIBDBlockRequests listens to appmessage.MsgRequestRelayBlocks messages and sends
// their corresponding blocks to the requesting peer.
func HandleIBDBlockRequests(context HandleIBDBlockRequestsContext, incomingRoute *router.Route,
	outgoingRoute *router.Route, peer *peerpkg.Peer,
) error {
	workerCount := runtime.NumCPU()
	if workerCount < 1 {
		workerCount = 1
	}
	// Cap workers to avoid saturating the serving node under large IBD batches.
	if workerCount > 8 {
		workerCount = 8
	}

	rateLimit := time.NewTicker(time.Second / time.Duration(workerCount))
	defer rateLimit.Stop()
	for {
		<-rateLimit.C // wait for rate limiter
		message, err := incomingRoute.Dequeue()
		if err != nil {
			return err
		}
		msgRequestIBDBlocks := message.(*appmessage.MsgRequestIBDBlocks)
		log.Debugf("Got request for %d ibd blocks", len(msgRequestIBDBlocks.Hashes))

		var done atomic.Bool
		jobs := make(chan *externalapi.DomainHash, len(msgRequestIBDBlocks.Hashes))
		var wg sync.WaitGroup
		wg.Add(workerCount)
		for i := 0; i < workerCount; i++ {
			go func() {
				defer wg.Done()
				for hash := range jobs {
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
						log.Warnf("IBD block %s not found", hash)
						done.Store(true)
						return
					}

					log.Debugf("Relaying IBD block %s to peer %s", hash, peer.Address())
					ibdBlockMessage := appmessage.NewMsgIBDBlock(appmessage.DomainBlockToMsgBlock(block))
					err = outgoingRoute.Enqueue(ibdBlockMessage)
					if err != nil {
						log.Warnf("failed to enqueue block %s: %s", hash, err)
						done.Store(true)
						return
					}
				}
			}()
		}

		for _, hash := range msgRequestIBDBlocks.Hashes {
			if done.Load() {
				break
			}
			jobs <- hash
		}
		close(jobs)
		wg.Wait()
		if done.Load() {
			return nil
		}
	}
}
