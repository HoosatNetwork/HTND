package protocol

import (
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/app/protocol/common"
	"github.com/HoosatNetwork/HTND/v2/app/protocol/flowcontext"
	peerpkg "github.com/HoosatNetwork/HTND/v2/app/protocol/peer"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/id"
	routerpkg "github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/router"
	"github.com/pkg/errors"
)

// TestPeerCanReconnectWhileOldFlowsFinish pins that a peer whose connection ended can connect again with the same ID
// at once, while a flow of the old connection is still running, as SendPings does until its next tick. The entry used
// to be removed only once every flow had returned, so the reconnect was refused with ErrPeerWithSameIDExists for up
// to two minutes. The old connection's late removal must then leave the new connection's entry alone.
func TestPeerCanReconnectWhileOldFlowsFinish(t *testing.T) {
	m := &Manager{context: flowcontext.New(nil, nil, nil, nil, nil)}
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
	if err := m.context.AddToPeers(oldPeer); err != nil {
		t.Fatalf("AddToPeers: %+v", err)
	}
	errChan := make(chan error, 1)
	releaseLingeringFlow := make(chan struct{})
	flows := []*common.Flow{
		{Name: "disconnected", ExecuteFunc: func(*peerpkg.Peer) { errChan <- errors.WithStack(routerpkg.ErrRouteClosed) }},
		{Name: "lingering", ExecuteFunc: func(*peerpkg.Peer) { <-releaseLingeringFlow }},
	}
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		// As in routerInitializer, which removes the peer once more when it returns.
		defer m.context.RemoveFromPeers(oldPeer)
		m.runFlowsUntilStopped(flows, oldPeer, errChan, routerpkg.NewRouter("old connection"), oldPeer.Connection())
	}()

	reconnectedPeer := newPeer()
	deadline := time.Now().Add(time.Second)
	for {
		err := m.context.AddToPeers(reconnectedPeer)
		if err == nil {
			break
		}
		if !errors.Is(err, common.ErrPeerWithSameIDExists) {
			t.Fatalf("AddToPeers: %+v", err)
		}
		if time.Now().After(deadline) {
			t.Fatal("the peer could not reconnect within 1 s of its connection ending")
		}
		time.Sleep(10 * time.Millisecond)
	}

	close(releaseLingeringFlow)
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the old connection's flows did not stop")
	}
	if m.context.GetPeer(reconnectedPeer) != reconnectedPeer {
		t.Fatal("the old connection's removal removed the reconnected peer's entry")
	}
}
