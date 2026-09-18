package rpcclient

import (
	"sync"
	"testing"
	"time"
)

// TestConcurrentRequestsDuringSetTimeoutDoNotRace is HTN-212: c.timeout was a plain time.Duration
// field, written by connect() (briefly, around the initial GetInfo call on every (re)connection) and
// by SetTimeout, with no synchronization at all, while every rpc_*.go request method read it
// unguarded to build a DequeueWithTimeout call. This runs a goroutine issuing GetInfo calls and
// another calling SetTimeout concurrently, under `go test -race` - the thing that actually catches
// this class of bug. A single successful run proves nothing by itself, but a run under -race on the
// pre-fix code (a plain field instead of the current atomic.Int64) reliably reports a DATA RACE on
// rpcClient.timeout.
//
// This intentionally does not also hammer Reconnect concurrently with active sends: doing so
// surfaced a second, separate, pre-existing race inside grpcclient.GRPCClient (Disconnect's
// CloseSend racing an in-flight send on the same gRPC stream, unrelated to c.rpcRouter/c.timeout) -
// recorded separately, not fixed here to keep this change scoped to the field-access race it targets.
// The pre-existing TestReconnectReleasesPreviousConnection and
// TestCloseWhileReconnectingDoesNotExitTheProcess tests already exercise Reconnect concurrently with
// this fix in place and pass under -race, which is the coverage this change relies on for the
// c.rpcRouter half of the same fix.
func TestConcurrentRequestsDuringSetTimeoutDoNotRace(t *testing.T) {
	address, _ := startGetInfoRPCServer(t)

	client, err := NewRPCClient(address)
	if err != nil {
		t.Fatalf("NewRPCClient: %+v", err)
	}
	defer client.Close()
	client.SetTimeout(2 * time.Second)

	const duration = 500 * time.Millisecond
	stop := time.Now().Add(duration)

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for time.Now().Before(stop) {
			// Only a race or a panic is a failure here - a request/timeout mismatch is not checked,
			// since correctness of the value isn't what this test is about.
			_, _ = client.GetInfo()
		}
	}()

	go func() {
		defer wg.Done()
		for time.Now().Before(stop) {
			client.SetTimeout(time.Duration(1+time.Now().UnixNano()%3) * time.Second)
		}
	}()

	wg.Wait()
}
