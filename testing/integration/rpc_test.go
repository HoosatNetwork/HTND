package integration

import (
	"errors"
	"runtime"
	"testing"
	"time"

	"github.com/Hoosat-Oy/HTND/infrastructure/config"
	routerpkg "github.com/Hoosat-Oy/HTND/infrastructure/network/netadapter/router"

	"github.com/Hoosat-Oy/HTND/infrastructure/network/rpcclient"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

const rpcTimeout = 10 * time.Second

type testRPCClient struct {
	*rpcclient.RPCClient
}

func newTestRPCClient(rpcAddress string) (*testRPCClient, error) {
	rpcClient, err := rpcclient.NewRPCClient(rpcAddress)
	if err != nil {
		return nil, err
	}
	rpcClient.SetTimeout(rpcTimeout)

	return &testRPCClient{
		RPCClient: rpcClient,
	}, nil
}

func connectAndClose(rpcAddress string) error {
	client, err := rpcclient.NewRPCClient(rpcAddress)
	if err != nil {
		return err
	}
	defer client.Close()
	return nil
}

func closeRPCClients(t *testing.T, clients []*testRPCClient) {
	t.Helper()
	for _, client := range clients {
		if client == nil {
			continue
		}
		err := client.Close()
		if err != nil && err.Error() != "Cannot close a client that had already been closed" {
			t.Fatalf("Failed to close RPC client: %s", err)
		}
	}
}

func TestRPCClientGoroutineLeak(t *testing.T) {
	_, teardown := setupHarness(t, &harnessParams{
		p2pAddress:              p2pAddress1,
		rpcAddress:              rpcAddress1,
		miningAddress:           miningAddress1,
		miningAddressPrivateKey: miningAddress1PrivateKey,
	})
	defer teardown()
	numGoroutinesBefore := runtime.NumGoroutine()
	for i := 1; i < 100; i++ {
		err := connectAndClose(rpcAddress1)
		if err != nil {
			t.Fatalf("Failed to set up an RPC client: %s", err)
		}
		time.Sleep(10 * time.Millisecond)
		if runtime.NumGoroutine() > numGoroutinesBefore+10 {
			t.Fatalf("Number of goroutines is increasing for each RPC client open (%d -> %d), which indicates a memory leak",
				numGoroutinesBefore, runtime.NumGoroutine())
		}
	}
}

func TestRPCMaxInboundConnections(t *testing.T) {
	harness, teardown := setupHarness(t, &harnessParams{
		p2pAddress:              p2pAddress1,
		rpcAddress:              rpcAddress1,
		miningAddress:           miningAddress1,
		miningAddressPrivateKey: miningAddress1PrivateKey,
	})
	defer teardown()

	// Close the default RPC client so that it won't interfere with the test
	err := harness.rpcClient.Close()
	if err != nil {
		t.Fatalf("Failed to close the default harness RPCClient: %s", err)
	}

	if harness.config.RPCMaxClients != config.DefaultMaxRPCClients {
		t.Fatalf("Unexpected RPC max clients mismatch: harness=%d default=%d", harness.config.RPCMaxClients, config.DefaultMaxRPCClients)
	}

	rpcClients := make([]*testRPCClient, 0, harness.config.RPCMaxClients)
	defer closeRPCClients(t, rpcClients)

	for i := 0; i < harness.config.RPCMaxClients; i++ {
		rpcClient, err := newTestRPCClient(harness.rpcAddress)
		if err != nil {
			t.Fatalf("newTestRPCClient #%d: %s", i+1, err)
		}
		rpcClients = append(rpcClients, rpcClient)
	}

	_, err = newTestRPCClient(harness.rpcAddress)
	if err == nil {
		t.Fatalf("newTestRPCClient unexpectedly succeeded after reaching the max inbound RPC client limit")
	}
	if errors.Is(err, routerpkg.ErrTimeout) {
		t.Fatalf("Rejected RPC client was misattributed to timeout instead of immediate rejection: %v", err)
	}

	var statusErr interface{ GRPCStatus() *grpcstatus.Status }
	if errors.As(err, &statusErr) {
		if statusErr.GRPCStatus().Code() != codes.ResourceExhausted {
			t.Fatalf("Expected ResourceExhausted for rejected RPC client, got: %s (%v)", statusErr.GRPCStatus().Code(), err)
		}
	} else if !errors.Is(err, routerpkg.ErrRouteClosed) {
		t.Fatalf("Expected rejected RPC client to fail with ResourceExhausted or closed route, got: %v", err)
	}

	err = rpcClients[0].Close()
	if err != nil {
		t.Fatalf("Failed to close one of the accepted RPC clients: %s", err)
	}
	rpcClients[0] = nil

	var replacementClient *testRPCClient
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		replacementClient, err = newTestRPCClient(harness.rpcAddress)
		if err == nil {
			rpcClients[0] = replacementClient
			return
		}
		time.Sleep(50 * time.Millisecond)
	}

	t.Fatalf("Timed out waiting for RPC slot cleanup after disconnect; last connect error: %v", err)
}
