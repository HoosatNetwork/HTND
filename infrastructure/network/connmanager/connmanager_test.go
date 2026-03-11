package connmanager

import "testing"

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
