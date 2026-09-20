package rpc

import (
	"math/big"
	"testing"

	"github.com/HoosatNetwork/HTND/app/rpc/rpccontext"
	"github.com/HoosatNetwork/HTND/domain"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/blockheader"
	"github.com/HoosatNetwork/HTND/domain/dagconfig"
	"github.com/HoosatNetwork/HTND/domain/miningmanager"
	"github.com/HoosatNetwork/HTND/infrastructure/network/netadapter/router"
)

// missingBlockConsensus reports every block as absent, which is what the live consensus does for a
// block belonging to the consensus that an IBD just replaced.
type missingBlockConsensus struct {
	externalapi.Consensus
}

func (missingBlockConsensus) GetBlockInfo(*externalapi.DomainHash) (*externalapi.BlockInfo, error) {
	// Exactly what consensus.GetBlockInfo returns for a block it does not have: Exists false and a
	// zero-valued BlockStatus, which is StatusInvalid.
	return &externalapi.BlockInfo{}, nil
}

type missingBlockDomain struct {
	domain.Domain
}

func (missingBlockDomain) Consensus() externalapi.Consensus { return missingBlockConsensus{} }

func (missingBlockDomain) MiningManager() miningmanager.MiningManager { return nil }

// TestBlockAddedNotificationSurvivesAnUndescribableBlock is HTN-225's regression test.
//
// notifyBlockAddedToDAG used to return the error from PopulateRPCBlockWithVerboseData to
// initConsensusEventsHandler, which panics on it - and since that handler runs on a spawned
// goroutine, panics.HandlePanic turns the panic into os.Exit(1). So a single block the node could
// not describe killed the whole node. It is reached in production at the end of an IBD: committing
// the staging consensus deletes the previous consensus' database prefix, and a BlockAdded event
// queued against the old consensus then resolves against the new one, which does not have the block.
//
// The test drives notifyBlockAddedToDAG directly rather than through the handler, because the old
// behaviour was to exit the process, which a test cannot survive to observe.
func TestBlockAddedNotificationSurvivesAnUndescribableBlock(t *testing.T) {
	notificationManager := rpccontext.NewNotificationManager(&dagconfig.MainnetParams)

	// A listener that wants block-added notifications, so the early "nobody is listening" return does
	// not hide the path under test.
	listenerRouter := router.NewRouter("block added notification test")
	notificationManager.AddListener(listenerRouter)
	listener, err := notificationManager.Listener(listenerRouter)
	if err != nil {
		t.Fatalf("Listener: %+v", err)
	}
	listener.PropagateBlockAddedNotifications()
	if !notificationManager.HasBlockAddedListeners() {
		t.Fatalf("the notification manager reports no block added listeners, so the test would not " +
			"reach PopulateRPCBlockWithVerboseData at all")
	}

	manager := &Manager{
		context: &rpccontext.Context{
			NotificationManager: notificationManager,
			Domain:              missingBlockDomain{},
			Config:              nil,
		},
	}

	block := &externalapi.DomainBlock{
		Header: blockheader.NewImmutableBlockHeader(
			1, []externalapi.BlockLevelParents{}, &externalapi.DomainHash{}, &externalapi.DomainHash{},
			&externalapi.DomainHash{}, 0, 0, 0, 0, 0, big.NewInt(0), &externalapi.DomainHash{}),
	}

	if err := manager.notifyBlockAddedToDAG(block); err != nil {
		t.Fatalf("notifyBlockAddedToDAG returned an error for a block the consensus does not have, "+
			"which initConsensusEventsHandler turns into a panic and panics.HandlePanic turns into "+
			"os.Exit(1) - the node would have died here: %+v", err)
	}
}
