package rpc

import (
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/app/rpc/rpccontext"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
)

// TestWaitForConsensusEventsHandlerDrainsQueuedEvents pins that shutdown can wait for the consensus events handler to
// handle every event queued before the events channel was closed. Shutdown used to close the database right after
// closing the channel, while the handler was still applying queued virtual changes to the UTXO index, so it read from a
// closed database, panicked and exited the node with status 1.
func TestWaitForConsensusEventsHandlerDrainsQueuedEvents(t *testing.T) {
	manager := &Manager{
		context: &rpccontext.Context{NotificationManager: rpccontext.NewNotificationManager(&dagconfig.MainnetParams)},
	}

	const queuedEvents = 10000
	events := make(chan externalapi.ConsensusEvent, queuedEvents)
	for range queuedEvents {
		events <- &externalapi.BlockAdded{Block: &externalapi.DomainBlock{}}
	}
	manager.initConsensusEventsHandler(events)
	close(events)

	if !manager.WaitForConsensusEventsHandler(10 * time.Second) {
		t.Fatalf("the consensus events handler did not exit after its channel was closed")
	}
	if remaining := len(events); remaining != 0 {
		t.Fatalf("WaitForConsensusEventsHandler returned with %d queued events still unhandled", remaining)
	}
}

// TestWaitForConsensusEventsHandlerTimesOut pins that the wait is bounded while the channel is still open.
func TestWaitForConsensusEventsHandlerTimesOut(t *testing.T) {
	manager := &Manager{
		context: &rpccontext.Context{NotificationManager: rpccontext.NewNotificationManager(&dagconfig.MainnetParams)},
	}
	events := make(chan externalapi.ConsensusEvent)
	manager.initConsensusEventsHandler(events)
	defer close(events)

	if manager.WaitForConsensusEventsHandler(50 * time.Millisecond) {
		t.Fatalf("WaitForConsensusEventsHandler reported the handler exited while its channel was still open")
	}
}
