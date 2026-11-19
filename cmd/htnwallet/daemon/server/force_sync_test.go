package server

import "testing"

// A burst of broadcasts before syncLoop takes the pending request asks for one sync, not one each, and a
// request made while a sync runs is kept for the next one. forceSync queues the request before it returns,
// so the channel can be checked right away; with the old unbuffered channel it never holds one.
func TestForceSyncCoalescesRequests(t *testing.T) {
	s := &server{forceSyncChan: newForceSyncChan()}
	for range 100 {
		s.forceSync()
	}
	if n := len(s.forceSyncChan); n != 1 {
		t.Fatalf("after 100 forceSync calls %d syncs are pending, want 1", n)
	}

	<-s.forceSyncChan // syncLoop takes the request and starts a sync
	for range 100 {
		s.forceSync() // broadcasts while that sync runs
	}
	if n := len(s.forceSyncChan); n != 1 {
		t.Fatalf("after forceSync calls during a sync %d syncs are pending, want 1", n)
	}
}
