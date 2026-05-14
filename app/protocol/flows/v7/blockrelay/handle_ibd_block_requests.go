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

	// Keep a small bound on in-flight blocks to avoid buffering thousands of
	// full block bodies in the shared outgoing route, which can lead to extreme
	// RAM/GC pressure and appear as a node "freeze".
	maxQueuedOutgoing := workerCount * 4
	if maxQueuedOutgoing < 16 {
		maxQueuedOutgoing = 16
	}
	if maxQueuedOutgoing > 128 {
		maxQueuedOutgoing = 128
	}

	outgoingBackpressurePollInterval := 5 * time.Millisecond
	outgoingBackpressureTimeout := 30 * time.Second

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
		doneChan := make(chan struct{})
		var doneOnce sync.Once
		cancel := func() {
			doneOnce.Do(func() {
				done.Store(true)
				close(doneChan)
			})
		}

		jobs := make(chan *externalapi.DomainHash, workerCount*2)
		results := make(chan appmessage.Message, workerCount*2)
		var workersWG sync.WaitGroup
		var senderWG sync.WaitGroup

		// Single sender goroutine applies backpressure to the shared outgoing route.
		senderWG.Add(1)
		go func() {
			defer senderWG.Done()
			for {
				select {
				case <-doneChan:
					return
				case msg, ok := <-results:
					if !ok {
						return
					}
					deadline := time.Now().Add(outgoingBackpressureTimeout)
					for outgoingRoute.Length() >= maxQueuedOutgoing {
						if done.Load() {
							return
						}
						if time.Now().After(deadline) {
							log.Warnf("timed out waiting for outgoing route to drain (len=%d cap=%d) while serving IBD to %s", outgoingRoute.Length(), outgoingRoute.Capacity(), peer.Address())
							cancel()
							return
						}
						time.Sleep(outgoingBackpressurePollInterval)
					}

					err := outgoingRoute.Enqueue(msg)
					if err != nil {
						log.Warnf("failed to enqueue IBD block message: %s", err)
						cancel()
						return
					}
				}
			}
		}()

		workersWG.Add(workerCount)
		for i := 0; i < workerCount; i++ {
			go func() {
				defer workersWG.Done()
				for {
					select {
					case <-doneChan:
						return
					case hash, ok := <-jobs:
						if !ok {
							return
						}
						block, found, err := context.Domain().Consensus().GetBlock(hash)
						if err != nil {
							log.Warnf("unable to fetch requested block hash %s: %s", hash, err)
							cancel()
							return
						}
						if !found {
							log.Warnf("IBD block %s not found", hash)
							cancel()
							return
						}

						log.Debugf("Relaying IBD block %s to peer %s", hash, peer.Address())
						ibdBlockMessage := appmessage.NewMsgIBDBlock(appmessage.DomainBlockToMsgBlock(block))
						select {
						case <-doneChan:
							return
						case results <- ibdBlockMessage:
						}
					}
				}
			}()
		}

	feedLoop:
		for _, hash := range msgRequestIBDBlocks.Hashes {
			select {
			case <-doneChan:
				break feedLoop
			case jobs <- hash:
			}
			if done.Load() {
				break
			}
		}
		close(jobs)
		workersWG.Wait()
		close(results)
		senderWG.Wait()

		if done.Load() {
			return nil
		}
	}
}
