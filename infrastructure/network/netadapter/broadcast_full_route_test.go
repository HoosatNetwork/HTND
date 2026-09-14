package netadapter

import (
	"testing"

	"github.com/HoosatNetwork/HTND/app/appmessage"
	"github.com/HoosatNetwork/HTND/infrastructure/network/netadapter/router"
)

// TestP2PBroadcastSkipsFullRoutes pins that one peer whose outgoing route is full does not stop a
// broadcast from reaching the other peers, and does not fail the broadcast. It used to return
// ErrRouteCapacityReached at the saturated peer, so the peers after it missed the message and the flow
// that broadcast failed - disconnecting its own, unrelated peer.
func TestP2PBroadcastSkipsFullRoutes(t *testing.T) {
	na := &NetAdapter{}
	full := &NetConnection{router: router.NewRouter("full")}
	healthy := &NetConnection{router: router.NewRouter("healthy")}

	fullRoute := full.router.OutgoingRoute()
	for fullRoute.Length() < fullRoute.Capacity() {
		if err := fullRoute.Enqueue(appmessage.NewMsgPing(1)); err != nil {
			t.Fatalf("filling the route: %+v", err)
		}
	}

	err := na.P2PBroadcast([]*NetConnection{full, healthy}, appmessage.NewMsgPing(2))
	if err != nil {
		t.Fatalf("broadcast failed because one peer's route was full: %+v", err)
	}
	if got := healthy.router.OutgoingRoute().Length(); got != 1 {
		t.Fatalf("the peer after the saturated one should have received the message, its route holds %d", got)
	}
}
