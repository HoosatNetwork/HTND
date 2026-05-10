package integration

import (
	"testing"
	"time"

	"github.com/Hoosat-Oy/HTND/infrastructure/network/addressmanager"
)

func TestAddressExchange(t *testing.T) {
	appHarness1, appHarness2, appHarness3, teardown := standardSetup(t)
	defer teardown()

	testAddress := "1.2.3.4:6789"
	err := addressmanager.AddAddressByIP(appHarness1.app.AddressManager(), testAddress, nil)
	if err != nil {
		t.Fatalf("Error adding address to addressManager: %+v", err)
	}

	connect(t, appHarness1, appHarness2)
	connect(t, appHarness2, appHarness3)

	deadline := time.After(defaultTimeout)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	var lastErr error
	for {
		peerAddresses, err := appHarness3.rpcClient.GetPeerAddresses()
		if err == nil {
			for _, peerAddress := range peerAddresses.Addresses {
				if peerAddress.Addr == testAddress {
					return
				}
			}
		} else {
			lastErr = err
		}

		select {
		case <-ticker.C:
		case <-deadline:
			if lastErr != nil {
				t.Fatalf("Timed out waiting for address exchange; last GetPeerAddresses error: %+v", lastErr)
			}
			t.Fatalf("Timed out waiting for address exchange; didn't find %s in appHarness3 peer addresses", testAddress)
		}
	}
}
