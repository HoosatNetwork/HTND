package server

import (
	"context"

	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/daemon/pb"
)

func (s *server) Shutdown(_ context.Context, _ *pb.ShutdownRequest) (*pb.ShutdownResponse, error) {
	s.lock.Lock()
	defer s.lock.Unlock()
	// Calls are serialized by s.lock and this is the only place that closes s.shutdown, so checking first
	// is enough. Closing unconditionally panicked on a repeated request, and the gRPC server does not
	// recover handler panics.
	select {
	case <-s.shutdown:
	default:
		close(s.shutdown)
	}
	return &pb.ShutdownResponse{}, nil
}
