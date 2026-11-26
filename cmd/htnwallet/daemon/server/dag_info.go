package server

import (
	"context"
	"sync"
	"time"

	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/router"
	"github.com/pkg/errors"
	"google.golang.org/grpc/status"
)

const maxAddressListingRPCTimeout = 30 * time.Second

type dagInfoRequests struct {
	init   sync.Once
	active chan struct{}
}

// getBlockDAGInfo owns the foreground client's DAG-info response route. The node does not
// tag replies with request IDs, so only one request can wait for a reply on this route.
func (s *server) getBlockDAGInfo(ctx context.Context) (*appmessage.GetBlockDAGInfoResponseMessage, error) {
	requests := &s.dagInfoRequests
	requests.init.Do(func() { requests.active = make(chan struct{}, 1) })

	var queueDeadline <-chan time.Time
	if ctx.Done() == nil {
		// Synchronous wallet operations must not wait indefinitely for a
		// pending request or a stream reset when the node is unreachable.
		timer := time.NewTimer(maxAddressListingRPCTimeout)
		defer timer.Stop()
		queueDeadline = timer.C
	}
	select {
	case requests.active <- struct{}{}:
	case <-ctx.Done():
		return nil, status.FromContextError(ctx.Err()).Err()
	case <-queueDeadline:
		return nil, errors.Wrap(router.ErrTimeout, "waiting for the DAG-info route")
	}
	if err := ctx.Err(); err != nil {
		<-requests.active
		return nil, status.FromContextError(err).Err()
	}

	type reply struct {
		info *appmessage.GetBlockDAGInfoResponseMessage
		err  error
	}
	result := make(chan reply, 1)
	go func() {
		defer func() { <-requests.active }()
		info, err := s.rpcClient.GetBlockDAGInfo()
		result <- reply{info, err}
		// A canceled caller leaves the worker waiting for its own reply. If the
		// underlying RPC also times out, discard its route before taking another request.
		if errors.Is(err, router.ErrTimeout) {
			if err := s.rpcClient.Reconnect(); err != nil {
				log.Warnf("Could not reset the DAG-info stream after a timeout: %s", err)
			}
		}
	}()

	select {
	case response := <-result:
		return response.info, response.err
	case <-ctx.Done():
		return nil, status.FromContextError(ctx.Err()).Err()
	}
}
