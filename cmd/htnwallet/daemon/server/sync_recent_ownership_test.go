package server

import (
	"testing"
	"time"
)

func TestRecentScannerOwnsBatchDuringCooldown(t *testing.T) {
	node := newRecentTestNode()
	s := newRescanTestServer(t, node, 4500, 20)
	collectRecentAt(t, s, node, -32*time.Second)
	collectRecentAt(t, s, node, 0)
	node.asked()

	const start = 3000
	setRecentTestTime(s, node, 2*time.Second)
	if !s.recentScannerOwnsBatch(start) {
		t.Fatal("the batch in cooldown lost its recent scanner assignment")
	}
	if s.shouldPollRecentBatch(start, false) {
		t.Error("the recent scanner would poll the batch before its next deadline")
	}
	s.nextRescanIndex = start
	s.rescanRoundEnd = 6000
	coolingAddress := rescanTestAddress(external, start)
	oldAddress := rescanTestAddress(external, 2000)
	node.clearCache()
	node.pay(coolingAddress, oldAddress)
	scanned, err := s.collectNextOldBatch()
	if err != nil || !scanned {
		t.Fatalf("old scanner completed %t, error %v", scanned, err)
	}
	if s.addressSet[coolingAddress] != nil || s.addressSet[oldAddress] == nil {
		t.Fatal("the old cursor should skip the batch owned by the recent scanner and poll batch 2000")
	}
}
