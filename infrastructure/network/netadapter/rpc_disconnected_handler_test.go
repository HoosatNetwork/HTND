package netadapter

import (
	"testing"

	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/router"
)

// TestRPCConnectedHandlerDoesNotClobberRouterInitializersDisconnectedHandler is part of the fix for
// the RPC notification listener leak (a disconnected RPC client's outgoing route kept getting a
// "Couldn't send message to closed route" attempt on every notification, forever).
//
// onRPCConnectedHandler used to unconditionally overwrite the disconnected handler with a no-op right
// after the RouterInitializer ran, discarding any real cleanup (like removing the RPC notification
// listener) the initializer had just registered via the exported SetOnDisconnectedHandler. This pins
// that a handler set by the RouterInitializer survives, and the no-op fallback only applies when the
// initializer didn't set one.
func TestRPCConnectedHandlerDoesNotClobberRouterInitializersDisconnectedHandler(t *testing.T) {
	na := &NetAdapter{}
	fired := false
	na.SetRPCRouterInitializer(func(_ *router.Router, netConnection *NetConnection) {
		netConnection.SetOnDisconnectedHandler(func() { fired = true })
	})

	connection := &disconnectableConnection{}
	connection.connected.Store(true)
	if err := na.onRPCConnectedHandler(connection); err != nil {
		t.Fatalf("onRPCConnectedHandler: %+v", err)
	}

	connection.Disconnect()

	if !fired {
		t.Fatalf("the RouterInitializer's disconnected handler was overwritten by onRPCConnectedHandler's " +
			"no-op fallback instead of being preserved")
	}
}

// TestRPCConnectedHandlerFallsBackToANoOpWhenNoneIsSet pins the other half: start() panics if
// onDisconnectedHandler is nil, so onRPCConnectedHandler must still install a no-op when the
// RouterInitializer didn't register anything of its own.
func TestRPCConnectedHandlerFallsBackToANoOpWhenNoneIsSet(t *testing.T) {
	na := &NetAdapter{}
	na.SetRPCRouterInitializer(func(*router.Router, *NetConnection) {})

	connection := &disconnectableConnection{}
	connection.connected.Store(true)
	if err := na.onRPCConnectedHandler(connection); err != nil {
		t.Fatalf("onRPCConnectedHandler: %+v", err)
	}

	// start() would have panicked already if onDisconnectedHandler were still nil at this point;
	// disconnecting just exercises the fallback handler itself doesn't panic either.
	connection.Disconnect()
}
