package server

import (
	"testing"

	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/daemon/pb"
)

// TestShutdownTwice pins that a repeated Shutdown request is harmless. The handler closed the shutdown
// channel unconditionally, so a second call - a client retrying while the graceful stop drains - panicked
// with "close of closed channel", and the gRPC server does not recover handler panics.
func TestShutdownTwice(t *testing.T) {
	s := &server{shutdown: make(chan struct{})}

	for range 2 {
		if _, err := s.Shutdown(nil, &pb.ShutdownRequest{}); err != nil {
			t.Fatalf("Shutdown: %+v", err)
		}
	}

	select {
	case <-s.shutdown:
	default:
		t.Fatalf("Shutdown did not signal the shutdown channel")
	}
}
