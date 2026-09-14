package rpcclient

import (
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/app/appmessage"
	"github.com/HoosatNetwork/HTND/infrastructure/network/netadapter/server/grpcserver/protowire"
	"github.com/HoosatNetwork/HTND/version"
	"google.golang.org/grpc"
)

// getInfoRPCServer answers GetInfo requests and ignores everything else.
type getInfoRPCServer struct {
	protowire.UnimplementedRPCServer
}

func (s *getInfoRPCServer) MessageStream(stream protowire.RPC_MessageStreamServer) error {
	for {
		request, err := stream.Recv()
		if err != nil {
			return nil
		}
		appMessage, err := request.ToAppMessage()
		if err != nil {
			return err
		}
		if _, ok := appMessage.(*appmessage.GetInfoRequestMessage); !ok {
			continue
		}
		response, err := protowire.FromAppMessage(appmessage.NewGetInfoResponseMessage(
			"", 0, version.Version(), false, true, true, 0, 0, ""))
		if err != nil {
			return err
		}
		if err := stream.Send(response); err != nil {
			return nil
		}
	}
}

// countingListener counts the TCP connections that are currently open on the server side.
type countingListener struct {
	net.Listener
	open *atomic.Int32
}

func (l countingListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	l.open.Add(1)
	return &countedConn{Conn: conn, open: l.open}, nil
}

type countedConn struct {
	net.Conn
	open      *atomic.Int32
	closeOnce sync.Once
}

func (c *countedConn) Close() error {
	c.closeOnce.Do(func() { c.open.Add(-1) })
	return c.Conn.Close()
}

func startGetInfoRPCServer(t *testing.T) (string, *atomic.Int32) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %+v", err)
	}
	open := &atomic.Int32{}
	server := grpc.NewServer()
	protowire.RegisterRPCServer(server, &getInfoRPCServer{})
	go func() { _ = server.Serve(countingListener{Listener: listener, open: open}) }()
	t.Cleanup(server.Stop)
	return listener.Addr().String(), open
}

// TestReconnectReleasesPreviousConnection pins that reconnecting closes the connection it replaces, and
// that the replaced connection's loops, which report errors once it is closed, do not tear down the new
// one. Reconnect used to only half-close the old stream, so every reconnect (htnminer reconnects on each
// request timeout) leaked a TCP connection and its transport goroutines, and the old stream's
// end-of-stream callback closed the current router and started another reconnect.
func TestReconnectReleasesPreviousConnection(t *testing.T) {
	address, openConnections := startGetInfoRPCServer(t)

	client, err := NewRPCClient(address)
	if err != nil {
		t.Fatalf("NewRPCClient: %+v", err)
	}
	defer client.Close()

	const reconnects = 3
	for range reconnects {
		if err := client.Reconnect(); err != nil {
			t.Fatalf("Reconnect: %+v", err)
		}
	}

	deadline := time.Now().Add(5 * time.Second)
	for openConnections.Load() != 1 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if open := openConnections.Load(); open != 1 {
		t.Fatalf("after %d reconnects the server has %d open connections, want 1", reconnects, open)
	}

	// Give callbacks from the replaced connections time to arrive before checking the current one.
	time.Sleep(300 * time.Millisecond)
	client.SetTimeout(2 * time.Second)
	if _, err := client.GetInfo(); err != nil {
		t.Fatalf("GetInfo after reconnecting: %+v", err)
	}
}
