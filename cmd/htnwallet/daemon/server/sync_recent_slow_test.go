package server

import (
	"testing"
	"time"
)

func TestSlowRecentPollFindsEarlyCheckedAddress(t *testing.T) {
	for _, name := range []string{"persistent", "transient"} {
		t.Run(name, func(t *testing.T) {
			node := newRecentTestNode()
			s := newRescanTestServer(t, node, 0, 0)
			address := rescanTestAddress(external, 0)
			collectRecentAt(t, s, node, -64*time.Second)
			collectRecentAt(t, s, node, -32*time.Second)
			node.asked()

			// The target is checked at t=0 and other work delays the response until t=10.
			node.setCheckTime(recentTestEpoch)
			s.now = func() time.Time {
				node.mu.Lock()
				defer node.mu.Unlock()
				if node.calls == 0 {
					return recentTestEpoch
				}
				return recentTestEpoch.Add(10 * time.Second)
			}
			mustCollectRecent(t, s)
			node.pay(address)
			var firstDiscovery time.Duration
			for tick := 12 * time.Second; tick <= 44*time.Second; tick += 2 * time.Second {
				if name == "transient" && tick == 40*time.Second {
					node.mu.Lock()
					delete(node.usable, address)
					node.mu.Unlock()
				}
				collectRecentAt(t, s, node, tick)
				if s.addressSet[address] != nil && firstDiscovery == 0 {
					firstDiscovery = tick
				}
			}
			if firstDiscovery != 32*time.Second {
				t.Errorf("first discovery at %s (0 means never), want the first tick after the negative expired (32s)", firstDiscovery)
			}
		})
	}
}
