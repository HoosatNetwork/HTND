package connmanager

import (
	"net"
	"testing"

	"github.com/Hoosat-Oy/HTND/infrastructure/config"
)

func TestNeedsMoreOutboundPeers(t *testing.T) {
	connectionManager := &ConnectionManager{
		activeOutgoing: map[string]struct{}{
			"127.0.0.1:16110": {},
		},
		targetOutgoing: 2,
	}

	if !connectionManager.needsMoreOutboundPeers() {
		t.Fatalf("needsMoreOutboundPeers() = false, want true when active outbound peers are below target")
	}

	connectionManager.activeOutgoing["127.0.0.2:16110"] = struct{}{}
	if connectionManager.needsMoreOutboundPeers() {
		t.Fatalf("needsMoreOutboundPeers() = true, want false when active outbound peers meet target")
	}
}

func TestAddressesMatchResolvesHostnames(t *testing.T) {
	connectionManager := &ConnectionManager{
		cfg: &config.Config{
			Lookup: func(host string) ([]net.IP, error) {
				switch host {
				case "trusted.example":
					return []net.IP{net.ParseIP("10.0.0.7")}, nil
				default:
					return nil, nil
				}
			},
		},
	}

	matches, err := connectionManager.addressesMatch("trusted.example:16110", "10.0.0.7:16110")
	if err != nil {
		t.Fatalf("addressesMatch returned error: %v", err)
	}
	if !matches {
		t.Fatalf("addressesMatch() = false, want true for hostname/IP equivalents")
	}

	matches, err = connectionManager.addressesMatch("trusted.example:16110", "10.0.0.7:17110")
	if err != nil {
		t.Fatalf("addressesMatch returned error for mismatched ports: %v", err)
	}
	if matches {
		t.Fatalf("addressesMatch() = true, want false for same host with different ports")
	}
}

func TestIsPermanentMatchesResolvedRequestedPeers(t *testing.T) {
	connectionManager := &ConnectionManager{
		cfg: &config.Config{
			Lookup: func(host string) ([]net.IP, error) {
				if host == "trusted.example" {
					return []net.IP{net.ParseIP("10.0.0.7")}, nil
				}
				return nil, nil
			},
		},
		activeRequested: map[string]*connectionRequest{
			"trusted.example:16110": {
				address:     "trusted.example:16110",
				isPermanent: true,
			},
		},
		pendingRequested: map[string]*connectionRequest{},
	}

	if !connectionManager.isPermanent("10.0.0.7:16110") {
		t.Fatalf("isPermanent() = false, want true for resolved trusted peer address")
	}

	if connectionManager.isPermanent("10.0.0.7:17110") {
		t.Fatalf("isPermanent() = true, want false for same IP on different port")
	}
}
