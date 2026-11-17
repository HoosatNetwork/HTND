package rpc

import (
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/HoosatNetwork/HTND/v2/app/rpc/rpccontext"
	"github.com/HoosatNetwork/HTND/v2/domain"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
	"github.com/HoosatNetwork/HTND/v2/domain/miningmanager"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/router"
	"github.com/pkg/errors"
)

// acceptanceDataConsensus answers GetBlocksAcceptanceData with err, or with empty acceptance data when err is nil.
type acceptanceDataConsensus struct {
	externalapi.Consensus
	err error
}

func (c *acceptanceDataConsensus) GetBlocksAcceptanceData(blockHashes []*externalapi.DomainHash) (
	[]externalapi.AcceptanceData, error,
) {
	if c.err != nil {
		return nil, c.err
	}
	return make([]externalapi.AcceptanceData, len(blockHashes)), nil
}

type acceptanceDataDomain struct {
	domain.Domain
	consensus *acceptanceDataConsensus
}

func (d acceptanceDataDomain) Consensus() externalapi.Consensus { return d.consensus }

func (acceptanceDataDomain) MiningManager() miningmanager.MiningManager { return nil }

// TestChainChangedNotificationSurvivesStaleChainBlock is the chain-changed counterpart of
// TestBlockAddedNotificationSurvivesAnUndescribableBlock. Committing a staging consensus after an IBD deletes the
// previous consensus' database prefix, and a virtual change queued against the old consensus then names chain blocks
// whose acceptance data the new one does not have. notifyVirtualSelectedParentChainChanged returned that NotFound
// error to initConsensusEventsHandler, which panics on it and, on its spawned goroutine, exits the process.
//
// Only NotFound is skipped; every other error is still returned.
func TestChainChangedNotificationSurvivesStaleChainBlock(t *testing.T) {
	notificationManager := rpccontext.NewNotificationManager(&dagconfig.MainnetParams)

	// A subscriber that asks for accepted transaction IDs, so that the acceptance data is read at all.
	listenerRouter := router.NewRouter("chain changed notification test")
	notificationManager.AddListener(listenerRouter)
	listener, err := notificationManager.Listener(listenerRouter)
	if err != nil {
		t.Fatalf("Listener: %+v", err)
	}
	listener.PropagateVirtualSelectedParentChainChangedNotifications(true)

	consensus := &acceptanceDataConsensus{}
	manager := &Manager{
		context: &rpccontext.Context{
			NotificationManager: notificationManager,
			Domain:              acceptanceDataDomain{consensus: consensus},
		},
	}
	changeSet := &externalapi.VirtualChangeSet{
		VirtualSelectedParentChainChanges: &externalapi.SelectedChainPath{
			Added: []*externalapi.DomainHash{externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{1})},
		},
	}
	received := func() []*appmessage.VirtualSelectedParentChainChangedNotificationMessage {
		var notifications []*appmessage.VirtualSelectedParentChainChangedNotificationMessage
		for listenerRouter.OutgoingRoute().Length() > 0 {
			message, err := listenerRouter.OutgoingRoute().DequeueWithTimeout(time.Second)
			if err != nil {
				t.Fatalf("Dequeue: %+v", err)
			}
			notifications = append(notifications,
				message.(*appmessage.VirtualSelectedParentChainChangedNotificationMessage))
		}
		return notifications
	}

	// The stale chain block: NotFound is skipped, and nothing is sent.
	consensus.err = errors.Wrap(database.ErrNotFound, "acceptance data not found")
	if err := manager.notifyVirtualSelectedParentChainChanged(changeSet); err != nil {
		t.Fatalf("a NotFound error from GetBlocksAcceptanceData was returned, which initConsensusEventsHandler "+
			"turns into a panic and the node exits: %+v", err)
	}
	if notifications := received(); len(notifications) != 0 {
		t.Fatalf("got %d notification(s) for a skipped event, want none", len(notifications))
	}

	// The next event is delivered.
	consensus.err = nil
	if err := manager.notifyVirtualSelectedParentChainChanged(changeSet); err != nil {
		t.Fatalf("notifyVirtualSelectedParentChainChanged: %+v", err)
	}
	if notifications := received(); len(notifications) != 1 {
		t.Fatalf("got %d notification(s) for the next event, want 1", len(notifications))
	}

	// Any other error stays fatal.
	otherErr := errors.New("disk read failed")
	consensus.err = otherErr
	if err := manager.notifyVirtualSelectedParentChainChanged(changeSet); !errors.Is(err, otherErr) {
		t.Fatalf("got %v for a non-NotFound error, want it returned", err)
	}
	if notifications := received(); len(notifications) != 0 {
		t.Fatalf("got %d notification(s) after a failed event, want none", len(notifications))
	}
}
