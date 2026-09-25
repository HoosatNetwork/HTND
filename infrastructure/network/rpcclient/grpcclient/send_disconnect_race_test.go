package grpcclient

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/server/grpcserver/protowire"
	"google.golang.org/grpc/metadata"
)

// overlapDetectingStream is a protowire.RPC_MessageStreamClient fake that records whether two calls
// into its Send-family methods (Send, which wraps SendMsg, and CloseSend) were ever observed running
// at the same time. grpc-go's own ClientStream doc calls this combination out explicitly: "it is not
// safe to call CloseSend concurrently with SendMsg." A short sleep inside the critical section widens
// the window so a missing lock shows up reliably instead of only occasionally under -race.
type overlapDetectingStream struct {
	busy    atomic.Bool
	overlap atomic.Bool
}

func (s *overlapDetectingStream) enterSendFamily() {
	if !s.busy.CompareAndSwap(false, true) {
		s.overlap.Store(true)
		return
	}
	time.Sleep(time.Millisecond)
	s.busy.Store(false)
}

func (s *overlapDetectingStream) Send(*protowire.HoosatdMessage) error {
	s.enterSendFamily()
	return nil
}

func (s *overlapDetectingStream) Recv() (*protowire.HoosatdMessage, error) {
	return &protowire.HoosatdMessage{}, nil
}

func (s *overlapDetectingStream) CloseSend() error {
	s.enterSendFamily()
	return nil
}

func (s *overlapDetectingStream) Header() (metadata.MD, error) { return nil, nil }
func (s *overlapDetectingStream) Trailer() metadata.MD         { return nil }
func (s *overlapDetectingStream) Context() context.Context     { return context.Background() }
func (s *overlapDetectingStream) SendMsg(interface{}) error    { return nil }
func (s *overlapDetectingStream) RecvMsg(interface{}) error    { return nil }

// TestPostAndDisconnectNeverOverlapOnTheUnderlyingStream is HTN-213's regression test.
//
// Post and Disconnect both reach into the same underlying gRPC stream (Send and CloseSend
// respectively). Before this fix, Disconnect held closeSendMutex around CloseSend but Post (and
// AttachRouter's send loop, which shares Post's code path via send()) called stream.Send with no
// synchronization at all, so a Post in flight during a Disconnect could run concurrently with it -
// exactly the pattern grpc-go documents as unsafe. This runs many concurrent Post/Disconnect pairs
// against a fake stream that flags any overlap it observes between the two, and fails if either the
// race detector or the fake's own overlap flag catches one.
func TestPostAndDisconnectNeverOverlapOnTheUnderlyingStream(t *testing.T) {
	stream := &overlapDetectingStream{}
	client := &GRPCClient{stream: stream}

	const iterations = 300
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			_, _ = client.Post(&protowire.HoosatdMessage{})
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			_ = client.Disconnect()
		}
	}()

	wg.Wait()

	if stream.overlap.Load() {
		t.Fatal("Post and Disconnect were observed calling into the underlying stream's " +
			"Send/CloseSend concurrently - GRPCClient must serialize every Send-family call " +
			"through the same mutex")
	}
}
