package addressmanager

import (
	"net"
	"testing"

	"github.com/HoosatNetwork/HTND/app/appmessage"
	"github.com/HoosatNetwork/HTND/util/mstime"
)

// TestAddAddressWithNegativeTimestamp pins that an address advertised with a timestamp before the Unix
// epoch is ignored. Peers supply the timestamp unchecked, and persisting it panicked in serializeAddress,
// so a single address message from any peer used to take the node down.
func TestAddAddressWithNegativeTimestamp(t *testing.T) {
	addressManager, teardown := newAddressManagerForTest(t, "TestAddAddressWithNegativeTimestamp")
	defer teardown()

	bogus := &appmessage.NetAddress{IP: net.ParseIP("8.8.8.8"), Port: 42421, Timestamp: mstime.UnixMilliseconds(-1)}
	if err := addressManager.AddAddresses(bogus); err != nil {
		t.Fatalf("AddAddresses: %+v", err)
	}
	if _, err := addressManager.IsBanned(bogus); err == nil {
		t.Fatalf("an address with a negative timestamp should not have been stored")
	}

	valid := appmessage.NewNetAddressIPPort(net.ParseIP("8.8.4.4"), 42421)
	if err := addressManager.AddAddresses(valid); err != nil {
		t.Fatalf("AddAddresses: %+v", err)
	}
	if _, err := addressManager.IsBanned(valid); err != nil {
		t.Fatalf("a valid address should have been stored: %+v", err)
	}
}
