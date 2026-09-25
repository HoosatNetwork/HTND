package grpcserver

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/server/grpcserver/protowire"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type silentP2PServer struct {
	protowire.UnimplementedP2PServer
}

// MessageStream accepts the stream and never sends anything until the client goes away.
func (silentP2PServer) MessageStream(stream protowire.P2P_MessageStreamServer) error {
	<-stream.Context().Done()
	return nil
}

// TestCloseSendDoesNotWaitForSilentPeer pins that closing an outbound connection does not wait for the
// peer to send something. receive() holds the stream's read lock for the whole blocking Recv, and
// closeSend used to take the write lock before closing the client connection, so disconnecting a
// silent peer blocked forever and left the connection registered.
func TestCloseSendDoesNotWaitForSilentPeer(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %+v", err)
	}
	server := grpc.NewServer()
	protowire.RegisterP2PServer(server, silentP2PServer{})
	go func() { _ = server.Serve(listener) }()
	defer server.Stop()

	clientConnection, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("NewClient: %+v", err)
	}
	stream, err := protowire.NewP2PClient(clientConnection).MessageStream(context.Background())
	if err != nil {
		t.Fatalf("MessageStream: %+v", err)
	}
	connection := newConnection(nil, listener.Addr().(*net.TCPAddr), stream, clientConnection)

	receiveReturned := make(chan struct{})
	go func() {
		_, _ = connection.receive()
		close(receiveReturned)
	}()
	// Let receive take the read lock and block in Recv.
	time.Sleep(200 * time.Millisecond)

	closed := make(chan struct{})
	go func() {
		connection.closeSend()
		close(closed)
	}()

	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatalf("closeSend waited for a peer that never sends")
	}
	select {
	case <-receiveReturned:
	case <-time.After(5 * time.Second):
		t.Fatalf("the blocked receive did not return after closeSend")
	}
}
