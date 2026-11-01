package grpcserver

import (
	"net"
	"testing"
)

func TestIsRPCIPAllowed(t *testing.T) {
	_, ipv4Network, err := net.ParseCIDR("198.51.100.0/24")
	if err != nil {
		t.Fatal(err)
	}
	_, ipv6Network, err := net.ParseCIDR("2001:db8::/32")
	if err != nil {
		t.Fatal(err)
	}
	allowed := []*net.IPNet{ipv4Network, ipv6Network}

	tests := []struct {
		name string
		ip   string
		want bool
	}{
		{name: "allowed IPv4", ip: "198.51.100.4", want: true},
		{name: "allowed IPv6", ip: "2001:db8:1::4", want: true},
		{name: "IPv4 outside network", ip: "203.0.113.4"},
		{name: "IPv6 outside network", ip: "2001:db9::4"},
		{name: "IPv4 loopback always allowed", ip: "127.0.0.1", want: true},
		{name: "IPv6 loopback always allowed", ip: "::1", want: true},
		{name: "mapped IPv4 allowed", ip: "::ffff:198.51.100.4", want: true},
		{name: "mapped IPv4 loopback allowed", ip: "::ffff:127.0.0.1", want: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isRPCIPAllowed(net.ParseIP(test.ip), allowed); got != test.want {
				t.Errorf("isRPCIPAllowed(%s) = %t, want %t", test.ip, got, test.want)
			}
		})
	}
}
