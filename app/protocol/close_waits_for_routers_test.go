package protocol

import (
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/app/protocol/flowcontext"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/router"
)

// TestCloseWaitsForARouterInitializedBeforeIt runs a router initializer, holds its flows goroutine back as the
// scheduler can, and then closes the protocol manager. The router was initialized before Close, so Close must wait
// for its flows goroutine.
func TestCloseWaitsForARouterInitializedBeforeIt(t *testing.T) {
	flowsGoroutine := make(chan func(), 1)
	originalSpawn := spawn
	spawn = func(_ string, f func()) { flowsGoroutine <- f }
	defer func() { spawn = originalSpawn }()

	m := &Manager{context: flowcontext.New(nil, nil, nil, nil, nil)}
	m.routerInitializer(router.NewRouter("initialized before Close"), &netadapter.NetConnection{})

	closed := make(chan struct{})
	go func() {
		m.Close()
		close(closed)
	}()
	// Release the flows goroutine only once Close has marked the manager closed: released earlier, it would run the
	// flows of a manager whose dependencies are nil.
	deadline := time.Now().Add(5 * time.Second)
	for m.isClosed.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("Close did not mark the manager closed")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-closed:
		t.Fatal("Close returned before the flows goroutine of a router initialized before it had run")
	case <-time.After(100 * time.Millisecond):
	}

	(<-flowsGoroutine)()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return after the flows goroutine finished")
	}
}
