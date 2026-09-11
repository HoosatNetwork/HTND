package grpcserver

import (
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/app/appmessage"
	"github.com/HoosatNetwork/HTND/infrastructure/network/netadapter/router"
	"github.com/HoosatNetwork/HTND/infrastructure/network/netadapter/server"
	"github.com/pkg/errors"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// A peer that failed on compression framing is dialed plain until the fallback expires, and only
// that peer: every other peer keeps gzip.
func TestCompressionFallbackRemembersAndExpires(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	fallback := newCompressionFallback()
	fallback.now = func() time.Time { return now }

	if !fallback.shouldCompress("peer:42421") {
		t.Fatal("a peer with no recorded failure must be dialed with gzip")
	}
	fallback.disableFor("peer:42421")
	if fallback.shouldCompress("peer:42421") {
		t.Fatal("a peer that failed on compression framing must be dialed without compression")
	}
	if !fallback.shouldCompress("other:42421") {
		t.Fatal("one peer's failure must not turn compression off for another")
	}

	now = now.Add(compressionFallbackDuration - time.Second)
	if fallback.shouldCompress("peer:42421") {
		t.Fatal("the fallback must hold for its whole duration")
	}
	now = now.Add(time.Second)
	if !fallback.shouldCompress("peer:42421") {
		t.Fatal("gzip must be tried again once the fallback expires")
	}
	if len(fallback.until) != 0 {
		t.Fatalf("an expired entry must be removed when it is looked up, %d left", len(fallback.until))
	}
}

// Peers that failed once and are never dialed again must not accumulate forever.
func TestCompressionFallbackPrunesExpiredEntries(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	fallback := newCompressionFallback()
	fallback.now = func() time.Time { return now }

	fallback.disableFor("never-redialed:42421")
	now = now.Add(compressionFallbackDuration)
	fallback.disableFor("recent:42421")

	if _, ok := fallback.until["never-redialed:42421"]; ok {
		t.Fatal("an expired entry must be pruned when another peer is recorded")
	}
	if _, ok := fallback.until["recent:42421"]; !ok {
		t.Fatal("the newly recorded peer must be kept")
	}
}

func TestIsCompressionFramingError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"grpc-go's framing failure", status.Error(codes.Internal, compressionFramingErrorMessage), true},
		{"same code, different failure", status.Error(codes.Internal, "grpc: some other internal error"), false},
		{"same text, different code", status.Error(codes.Unimplemented, compressionFramingErrorMessage), false},
		{"not a status error", errors.New(compressionFramingErrorMessage), false},
		{"no error", nil, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isCompressionFramingError(test.err); got != test.want {
				t.Fatalf("isCompressionFramingError(%v) = %t, want %t", test.err, got, test.want)
			}
		})
	}
}

func TestP2PStreamCallOptionsRequestGzipOnlyWhenAsked(t *testing.T) {
	requestsGzip := func(options []grpc.CallOption) bool {
		for _, option := range options {
			if compressor, ok := option.(grpc.CompressorCallOption); ok && compressor.CompressorType == "gzip" {
				return true
			}
		}
		return false
	}
	if !requestsGzip(p2pStreamCallOptions(true)) {
		t.Fatal("a compressed outbound stream must request gzip")
	}
	if requestsGzip(p2pStreamCallOptions(false)) {
		t.Fatal("a fallback outbound stream must not request any compression")
	}
}

// Only an outbound stream that requested gzip records a fallback. An inbound peer chose its own
// encoding, and a plain stream that still fails proves the fault is the peer's, so neither changes
// how that peer is dialed.
func TestHandleCompressionFramingErrorRecordsOnlyCompressedOutboundFailures(t *testing.T) {
	clientConnection, err := grpc.NewClient("127.0.0.1:1", grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient: %+v", err)
	}
	defer clientConnection.Close()

	framingError := status.Error(codes.Internal, compressionFramingErrorMessage)
	tests := []struct {
		name           string
		outbound       bool
		compressed     bool
		wantCompressed bool
	}{
		{"outbound gzip stream", true, true, false},
		{"outbound plain stream", true, false, true},
		{"inbound stream", false, false, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			grpcServer := &gRPCServer{compressionFallback: newCompressionFallback()}
			connection := &gRPCConnection{
				server:      grpcServer,
				address:     &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 42421},
				dialAddress: "127.0.0.1:42421",
				compressed:  test.compressed,
			}
			if test.outbound {
				connection.lowLevelClientConnection = clientConnection
			}
			connection.handleCompressionFramingError(framingError)
			if got := grpcServer.compressionFallback.shouldCompress("127.0.0.1:42421"); got != test.wantCompressed {
				t.Fatalf("after the failure, shouldCompress = %t, want %t", got, test.wantCompressed)
			}
		})
	}
}

// End to end over real sockets: one listening P2P server must accept a gzip dialer and a plain
// dialer alike, and a dialer must open a plain stream to a peer it has in fallback.
func TestP2PServerSupportsCompressedAndUncompressedPeers(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("finding a free port: %+v", err)
	}
	address := listener.Addr().String()
	listener.Close()

	inboundRoutes := make(chan *router.Route, 2)
	listening, err := NewP2PServer([]string{address})
	if err != nil {
		t.Fatalf("NewP2PServer: %+v", err)
	}
	listening.SetOnConnectedHandler(func(connection server.Connection) error {
		inboundRouter := router.NewRouter(fmt.Sprintf("inbound %s", connection))
		route, err := inboundRouter.AddIncomingRoute("ping", []appmessage.MessageCommand{appmessage.CmdPing})
		if err != nil {
			return err
		}
		connection.SetOnDisconnectedHandler(inboundRouter.Close)
		connection.Start(inboundRouter)
		inboundRoutes <- route
		return nil
	})
	if err := listening.Start(); err != nil {
		t.Fatalf("Start: %+v", err)
	}
	defer listening.Stop()

	dialing, err := NewP2PServer(nil)
	if err != nil {
		t.Fatalf("NewP2PServer: %+v", err)
	}
	dialing.SetOnConnectedHandler(func(server.Connection) error { return nil })
	dialer := dialing.(*p2pServer)

	for i, compress := range []bool{true, false} {
		nonce := uint64(i + 1)
		if !compress {
			dialer.compressionFallback.disableFor(address)
		}

		connection, err := dialing.Connect(address)
		if err != nil {
			t.Fatalf("compress=%t: Connect: %+v", compress, err)
		}
		if got := connection.(*gRPCConnection).compressed; got != compress {
			t.Fatalf("compress=%t: the stream was opened with compressed=%t", compress, got)
		}
		outboundRouter := router.NewRouter("outbound")
		connection.SetOnDisconnectedHandler(outboundRouter.Close)
		connection.Start(outboundRouter)
		if err := outboundRouter.OutgoingRoute().Enqueue(appmessage.NewMsgPing(nonce)); err != nil {
			t.Fatalf("compress=%t: Enqueue: %+v", compress, err)
		}

		var inbound *router.Route
		select {
		case inbound = <-inboundRoutes:
		case <-time.After(10 * time.Second):
			t.Fatalf("compress=%t: the listening server never saw the connection", compress)
		}
		message, err := inbound.DequeueWithTimeout(10 * time.Second)
		if err != nil {
			t.Fatalf("compress=%t: the ping never arrived: %+v", compress, err)
		}
		if ping, ok := message.(*appmessage.MsgPing); !ok || ping.Nonce != nonce {
			t.Fatalf("compress=%t: expected ping %d, got %v", compress, nonce, message)
		}
		// End the connection the way a dropped peer does: close the transport underneath it, so the
		// receive loop's Recv returns and the connection tears itself down. Calling Disconnect here
		// instead would block for as long as the other side stays silent - it waits for the stream
		// lock that receive() holds for the whole of a parked Recv - and in this test it never speaks.
		_ = connection.(*gRPCConnection).lowLevelClientConnection.Close()
	}
}
