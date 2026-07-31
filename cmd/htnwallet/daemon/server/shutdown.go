package server

import (
	"context"

	"github.com/HoosatNetwork/HTND/cmd/htnwallet/daemon/pb"
)

func (s *server) Shutdown(_ context.Context, _ *pb.ShutdownRequest) (*pb.ShutdownResponse, error) {
	s.lock.Lock()
	defer s.lock.Unlock()
	close(s.shutdown)
	return &pb.ShutdownResponse{}, nil
}
