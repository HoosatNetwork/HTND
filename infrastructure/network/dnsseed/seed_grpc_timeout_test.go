package dnsseed

import (
	"context"
	"net"
	"testing"
	"time"

	pb2 "github.com/HoosatNetwork/HTND/infrastructure/network/dnsseed/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type silentPeerService struct {
	pb2.UnimplementedPeerServiceServer
}

// GetPeersList accepts the call and never answers until the caller gives up.
func (silentPeerService) GetPeersList(ctx context.Context, _ *pb2.GetPeersListRequest) (*pb2.GetPeersListResponse, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// TestRequestGRPCPeersTimesOut pins that asking a gRPC seed which never answers returns once the
// timeout passes, instead of blocking the seeding goroutine forever.
func TestRequestGRPCPeersTimesOut(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %+v", err)
	}
	server := grpc.NewServer()
	pb2.RegisterPeerServiceServer(server, silentPeerService{})
	go func() { _ = server.Serve(listener) }()
	defer server.Stop()

	done := make(chan error, 1)
	go func() {
		_, err := requestGRPCPeers(listener.Addr().String(), &pb2.GetPeersListRequest{}, 200*time.Millisecond)
		done <- err
	}()

	select {
	case err := <-done:
		if status.Code(err) != codes.DeadlineExceeded {
			t.Fatalf("expected DeadlineExceeded, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("requestGRPCPeers did not return for a seed that never answers")
	}
}
