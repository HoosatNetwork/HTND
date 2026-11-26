package server

import (
	"testing"
	"time"
)

func TestSmallWalletRecentPollingTraffic(t *testing.T) {
	checkCalls := func(t *testing.T, node *usableAddressesNode, stage string, want int) {
		t.Helper()
		calls, _ := node.asked()
		t.Logf("%s: %d GetUsableAddresses calls", stage, calls)
		if calls != want {
			t.Errorf("%s: made %d calls, want %d", stage, calls, want)
		}
	}

	t.Run("derived", func(t *testing.T) {
		node := newRecentTestNode()
		s := newRescanTestServer(t, node, 1000, 930)
		collectRecentAt(t, s, node, 0)
		checkCalls(t, node, "initial sweep", 2)

		setRecentTestTime(s, node, 12*time.Second)
		mustTick(t, s)
		checkCalls(t, node, "startup tick at 12s", 3)

		setRecentTestTime(s, node, 32*time.Second)
		mustTick(t, s)
		checkCalls(t, node, "first tick after startup verification", 3)

		setRecentTestTime(s, node, 34*time.Second)
		mustSync(t, func() error { return s.sync(true) })
		checkCalls(t, node, "forced sync at 34s", 3)

		setRecentTestTime(s, node, 36*time.Second)
		mustTick(t, s)
		checkCalls(t, node, "quiet tick at 36s", 1)

		// At the next full poll only the second recent batch takes 10s to reply.
		setRecentTestTime(s, node, 64*time.Second)
		s.now = func() time.Time {
			node.mu.Lock()
			defer node.mu.Unlock()
			if node.calls >= 3 {
				return recentTestEpoch.Add(74 * time.Second)
			}
			return recentTestEpoch.Add(64 * time.Second)
		}
		mustTick(t, s)
		checkCalls(t, node, "full poll at 64s with one slow reply", 3)

		setRecentTestTime(s, node, 76*time.Second)
		mustTick(t, s)
		checkCalls(t, node, "first tick after the slow reply", 2)
	})

	t.Run("imported", func(t *testing.T) {
		node := newRecentTestNode()
		s := newWebWalletServer(t)
		s.backgroundRPCClient = usableAddressesNodeClient(t, node)
		collectRecentAt(t, s, node, 0)
		checkCalls(t, node, "initial sweep", 1)

		setRecentTestTime(s, node, 32*time.Second)
		mustTick(t, s)
		checkCalls(t, node, "periodic tick at 32s", 2)

		setRecentTestTime(s, node, 34*time.Second)
		mustTick(t, s)
		checkCalls(t, node, "periodic tick at 34s", 2)
	})
}
