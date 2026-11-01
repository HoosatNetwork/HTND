package flowcontext

import (
	"testing"

	peerpkg "github.com/HoosatNetwork/HTND/v2/app/protocol/peer"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/id"
)

// TestBroadcastExceptLeavesOutExcludedPeer pins the connections a broadcast goes to: every ready peer for
// Broadcast, and every ready peer but the excluded one for BroadcastExcept.
func TestBroadcastExceptLeavesOutExcludedPeer(t *testing.T) {
	f := New(nil, nil, nil, &netadapter.NetAdapter{}, nil)
	peers := make([]*peerpkg.Peer, 3)
	for i := range peers {
		peerID, err := id.GenerateID()
		if err != nil {
			t.Fatalf("GenerateID: %+v", err)
		}
		peers[i] = peerpkg.New(&netadapter.NetConnection{})
		f.peers[*peerID] = peers[i]
	}

	check := func(excluded *peerpkg.Peer, want ...*peerpkg.Peer) {
		got := make(map[*netadapter.NetConnection]bool)
		for _, connection := range f.readyPeerConnections(excluded) {
			got[connection] = true
		}
		if len(got) != len(want) {
			t.Fatalf("broadcast goes to %d peers, want %d", len(got), len(want))
		}
		for _, peer := range want {
			if !got[peer.Connection()] {
				t.Fatalf("broadcast leaves out a peer it should reach")
			}
		}
	}
	check(nil, peers...)
	check(peers[1], peers[0], peers[2])
}

// TestRemoveFromPeersLeavesANewerEntryAlone pins that removing a peer leaves alone an entry that a newer connection
// of the same peer has added since, so that a late removal by the old connection cannot drop the new one.
func TestRemoveFromPeersLeavesANewerEntryAlone(t *testing.T) {
	f := New(nil, nil, nil, &netadapter.NetAdapter{}, nil)
	peerID, err := id.GenerateID()
	if err != nil {
		t.Fatalf("GenerateID: %+v", err)
	}
	newPeer := func() *peerpkg.Peer {
		connection := &netadapter.NetConnection{}
		connection.SetID(peerID)
		return peerpkg.New(connection)
	}

	oldPeer := newPeer()
	if err := f.AddToPeers(oldPeer); err != nil {
		t.Fatalf("AddToPeers: %+v", err)
	}
	f.RemoveFromPeers(oldPeer)
	reconnectedPeer := newPeer()
	if err := f.AddToPeers(reconnectedPeer); err != nil {
		t.Fatalf("AddToPeers after RemoveFromPeers: %+v", err)
	}

	f.RemoveFromPeers(oldPeer)
	if f.GetPeer(reconnectedPeer) != reconnectedPeer {
		t.Fatal("removing the old peer removed the reconnected peer's entry")
	}
	f.RemoveFromPeers(reconnectedPeer)
	if f.HasPeers() {
		t.Fatal("removing the reconnected peer left its entry")
	}
}
