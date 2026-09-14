package addressmanager

import (
	"net"
	"testing"

	"github.com/HoosatNetwork/HTND/app/appmessage"
	"github.com/HoosatNetwork/HTND/util/mstime"
)

// TestMalformedIPLengthIsNotRoutable pins that an address whose IP is neither 4 nor 16 bytes is rejected. Peers
// supply the IP bytes unchecked. Such a slice matched none of the loopback or private-range checks, so it counted as
// routable and was stored; an empty IP then rendered as ":<port>", which the node dialed on its own local system.
func TestMalformedIPLengthIsNotRoutable(t *testing.T) {
	addressManager, teardown := newAddressManagerForTest(t, "TestMalformedIPLengthIsNotRoutable")
	defer teardown()

	malformed := map[string]net.IP{
		"empty":    {},
		"3 bytes":  {8, 8, 8},
		"5 bytes":  {8, 8, 8, 8, 8},
		"17 bytes": append(net.ParseIP("2001:4860:4860::8888"), 1),
	}
	for name, ip := range malformed {
		address := &appmessage.NetAddress{IP: ip, Port: 42421, Timestamp: mstime.Now()}
		if IsValid(address) {
			t.Errorf("%s: IsValid accepted a malformed IP", name)
		}
		if IsRoutable(address, false) {
			t.Errorf("%s: IsRoutable accepted a malformed IP", name)
		}
		if IsRoutable(address, true) {
			t.Errorf("%s: IsRoutable with unroutable addresses accepted a malformed IP", name)
		}
		if err := addressManager.AddAddresses(address); err != nil {
			t.Fatalf("%s: AddAddresses: %+v", name, err)
		}
	}
	for _, stored := range addressManager.Addresses() {
		host, _, err := net.SplitHostPort(stored.TCPAddress().String())
		if err != nil || host == "" || net.ParseIP(host) == nil {
			t.Fatalf("stored an address that does not dial a real host: %q", stored.TCPAddress().String())
		}
	}

	for _, ip := range []net.IP{net.ParseIP("8.8.8.8"), net.ParseIP("8.8.8.8").To4(), net.ParseIP("2001:4860:4860::8888")} {
		address := &appmessage.NetAddress{IP: ip, Port: 42421, Timestamp: mstime.Now()}
		if !IsRoutable(address, false) {
			t.Errorf("IsRoutable rejected the well-formed address %s (%d bytes)", ip, len(ip))
		}
	}
}
