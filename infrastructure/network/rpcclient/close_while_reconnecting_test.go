package rpcclient

import (
	"net"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/infrastructure/logger"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/server/grpcserver/protowire"
	"google.golang.org/grpc"
)

const closeWhileReconnectingChildEnv = "HTND_RPCCLIENT_CLOSE_WHILE_RECONNECTING_CHILD"

// TestCloseWhileReconnectingDoesNotExitTheProcess pins that closing an RPCClient while it is reconnecting shuts
// it down instead of ending the process. When its node went away the receive loop called Reconnect, and
// handleClientDisconnected panicked on Reconnect's "client was closed" error once Close ran. The loop runs in a
// spawned goroutine whose recovered panic calls os.Exit(1), so a wallet daemon or miner stopping during a node
// outage exited with status 1. The scenario runs in a child process so the exit can be observed.
func TestCloseWhileReconnectingDoesNotExitTheProcess(t *testing.T) {
	if os.Getenv(closeWhileReconnectingChildEnv) == "1" {
		closeWhileReconnecting(t)
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestCloseWhileReconnectingDoesNotExitTheProcess$", "-test.count=1")
	cmd.Env = append(os.Environ(), closeWhileReconnectingChildEnv+"=1")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("closing a reconnecting RPC client ended the process (%v):\n%s", err, output)
	}
}

func closeWhileReconnecting(t *testing.T) {
	// Warnings reach the parent's failure output, including the reason a spawned goroutine exited the process.
	logger.SetLogLevels(logger.LevelWarn)
	logger.InitLogStdout(logger.LevelWarn)
	reconnectRetryDelay = 20 * time.Millisecond

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %+v", err)
	}
	server := grpc.NewServer()
	protowire.RegisterRPCServer(server, &getInfoRPCServer{})
	go func() { _ = server.Serve(listener) }()

	client, err := NewRPCClient(listener.Addr().String())
	if err != nil {
		t.Fatalf("NewRPCClient: %+v", err)
	}

	// Take the node away: the client's receive loop fails and starts reconnecting, which keeps failing.
	server.Stop()
	waitFor(t, "the client to start reconnecting", func() bool { return client.isReconnecting.Load() == 1 })

	_ = client.Close()

	// Reconnect returns once it sees the client was closed; handleClientDisconnected acts on that right after.
	waitFor(t, "Reconnect to return", func() bool { return client.isReconnecting.Load() == 0 })
	time.Sleep(200 * time.Millisecond)
}

func waitFor(t *testing.T, what string, condition func() bool) {
	// Longer than one connection attempt, which can wait up to grpcclient's 30s stream setup timeout.
	deadline := time.Now().Add(2 * time.Minute)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
