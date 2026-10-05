package rpcclient

import (
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/router"
	"github.com/pkg/errors"
)

// TestAddressRequestsTimeOut pins that GetMempoolEntriesByAddresses and GetBalancesByAddresses give up after
// the client's timeout when the node does not answer. Both waited without one; htnwallet refreshes its UTXOs
// with GetMempoolEntriesByAddresses while holding its server lock, so a stalled node hung the wallet.
func TestAddressRequestsTimeOut(t *testing.T) {
	// This server answers GetInfo, so the client connects, and ignores every other request.
	address, _ := startGetInfoRPCServer(t)
	client, err := NewRPCClient(address)
	if err != nil {
		t.Fatalf("NewRPCClient: %+v", err)
	}
	defer client.Close()
	client.SetTimeout(500 * time.Millisecond)

	addresses := []string{"hoosattest:unused"}
	requests := map[string]func() error{
		"GetMempoolEntriesByAddresses": func() error {
			_, err := client.GetMempoolEntriesByAddresses(addresses, true, false)
			return err
		},
		"GetBalancesByAddresses": func() error {
			_, err := client.GetBalancesByAddresses(addresses)
			return err
		},
	}
	for name, request := range requests {
		done := make(chan error, 1)
		go func() { done <- request() }()

		select {
		case err := <-done:
			if !errors.Is(err, router.ErrTimeout) {
				t.Fatalf("%s: expected a timeout error, got %+v", name, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s did not return while the node was not answering", name)
		}
	}
}
