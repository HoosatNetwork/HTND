package server

import (
	"testing"
	"time"
)

func TestOldCursorKeepsPendingRecentHandoff(t *testing.T) {
	for _, tc := range []struct {
		name     string
		evict    bool
		wantTick int
	}{
		{name: "recent deadline", wantTick: 32},
		{name: "old recheck after eviction", evict: true, wantTick: 54},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node := newRecentTestNode()
			s := newRescanTestServer(t, node, 6500, 22000)
			collectRecentAt(t, s, node, -32*time.Second)
			collectRecentAt(t, s, node, 0)
			s.nextRescanIndex = 16000
			s.rescanRoundEnd = 23000
			if err := s.keysFile.SetLastUsedExternalIndex(8500); err != nil {
				t.Fatal(err)
			}

			address := rescanTestAddress(external, 5050)
			var discovered int
			for tick := 2; tick <= 56; tick += 2 {
				switch {
				case !tc.evict && tick == 2:
					node.pay(address)
				case tc.evict && tick == 22:
					node.clearCache() // The old cursor checks and caches a fresh negative.
				case tc.evict && tick == 24:
					node.pay(address)
				}
				setRecentTestTime(s, node, time.Duration(tick)*time.Second)
				mustTick(t, s)
				if discovered == 0 && s.addressSet[address] != nil {
					discovered = tick
				}
			}
			if discovered != tc.wantTick {
				t.Errorf("first discovery at %ds (0 means never), want %ds", discovered, tc.wantTick)
			}
		})
	}
}
