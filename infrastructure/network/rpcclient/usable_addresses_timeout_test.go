package rpcclient

import (
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/infrastructure/network/netadapter/router"
	"github.com/pkg/errors"
)

// TestGetUsableAddressesTimesOut pins that GetUsableAddresses gives up after the client's timeout when the
// node does not answer. It waited without a timeout, and htnwallet makes the call while holding its server
// lock, so a stalled node hung the wallet's sync loop and every wallet RPC that needed the lock.
func TestGetUsableAddressesTimesOut(t *testing.T) {
	// This server answers GetInfo, so the client connects, and ignores GetUsableAddresses.
	address, _ := startGetInfoRPCServer(t)
	client, err := NewRPCClient(address)
	if err != nil {
		t.Fatalf("NewRPCClient: %+v", err)
	}
	defer client.Close()
	client.SetTimeout(500 * time.Millisecond)

	done := make(chan error, 1)
	go func() {
		_, err := client.GetUsableAddresses([]string{"hoosattest:unused"})
		done <- err
	}()

	select {
	case err := <-done:
		if !errors.Is(err, router.ErrTimeout) {
			t.Fatalf("expected a timeout error, got %+v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("GetUsableAddresses did not return while the node was not answering")
	}
}
