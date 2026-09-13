package blockrelay

import (
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/HoosatNetwork/HTND/app/appmessage"
	peerpkg "github.com/HoosatNetwork/HTND/app/protocol/peer"
	"github.com/HoosatNetwork/HTND/app/protocol/protocolerrors"
	"github.com/HoosatNetwork/HTND/domain"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/infrastructure/network/netadapter/router"
)

// RelayBlockRequestsContext is the interface for the context needed for the HandleRelayBlockRequests flow.
type RelayBlockRequestsContext interface {
	Domain() domain.Domain
}

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
		message, err := incomingRoute.Dequeue()
		if err != nil {
			return err
		}
		getRelayBlocksMessage := message.(*appmessage.MsgRequestRelayBlocks)
		err = serveRequestedBlocks(getRelayBlocksMessage.Hashes, semaphore, "HandleRelayBlockRequests-worker",
			func(hash *externalapi.DomainHash) error {
				block, found, err := context.Domain().Consensus().GetBlock(hash)
				if err != nil {
					return protocolerrors.Errorf(false, "unable to fetch requested relay block %s: %s", hash, err)
				}
				if !found {
					return protocolerrors.Errorf(false, "relay block %s not found", hash)
				}

				log.Debugf("Relaying block %s to peer %s", hash, peer.Address())
				return outgoingRoute.Enqueue(appmessage.DomainBlockToMsgBlock(block))
			})
		if err != nil {
			return err
		}
	}
}

// serveRequestedBlocks runs serve for each hash, at most cap(semaphore) at a time, and returns the first
// error once every worker it started has finished.
//
// Waiting matters. The workers used to only set a flag on failure, which the dispatch loop checked
// before starting the next hash: for the last or only hash of a request the flag was set after that
// check, nothing was sent and nothing was returned, and the requesting peer waited out its timeout on a
// connection that looked healthy. Errors from serve are expected to be protocol errors formatted rather
// than wrapped, so that FlowContext.HandleError disconnects the peer instead of dropping a not-found.
func serveRequestedBlocks(hashes []*externalapi.DomainHash, semaphore chan struct{}, workerName string,
	serve func(hash *externalapi.DomainHash) error,
) error {
	var (
		wg       sync.WaitGroup
		failed   atomic.Bool
		errOnce  sync.Once
		firstErr error
	)
	for _, hash := range hashes {
		if failed.Load() {
			break
		}
		semaphore <- struct{}{} // acquire
		wg.Add(1)
		spawn(workerName, func() {
			defer wg.Done()
			defer func() { <-semaphore }() // release
			if failed.Load() {
				return
			}
			err := serve(hash)
			if err != nil {
				errOnce.Do(func() {
					firstErr = err
					failed.Store(true)
				})
			}
		})
	}
	wg.Wait()
	return firstErr
}
