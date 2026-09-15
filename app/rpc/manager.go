package rpc

import (
	"time"

	"github.com/HoosatNetwork/HTND/app/appmessage"
	"github.com/HoosatNetwork/HTND/app/protocol"
	"github.com/HoosatNetwork/HTND/app/rpc/rpccontext"
	"github.com/HoosatNetwork/HTND/domain"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/utxoindex"
	"github.com/HoosatNetwork/HTND/infrastructure/config"
	"github.com/HoosatNetwork/HTND/infrastructure/logger"
	"github.com/HoosatNetwork/HTND/infrastructure/network/addressmanager"
	"github.com/HoosatNetwork/HTND/infrastructure/network/connmanager"
	"github.com/HoosatNetwork/HTND/infrastructure/network/netadapter"
	"github.com/pkg/errors"
)

// Manager is an RPC manager
type Manager struct {
	context             *rpccontext.Context
	consensusEventsChan chan externalapi.ConsensusEvent

	// consensusEventsHandlerDone is closed when the consensus events handler has exited, after handling every event
	// queued before the events channel was closed.
	consensusEventsHandlerDone chan struct{}
}

// NewManager creates a new RPC Manager
func NewManager(
	cfg *config.Config,
	domain domain.Domain,
	netAdapter *netadapter.NetAdapter,
	protocolManager *protocol.Manager,
	connectionManager *connmanager.ConnectionManager,
	addressManager *addressmanager.AddressManager,
	utxoIndex *utxoindex.UTXOIndex,
	consensusEventsChan chan externalapi.ConsensusEvent,
	shutDownChan chan<- struct{},
) *Manager {
	manager := Manager{
		context: rpccontext.NewContext(
			cfg,
			domain,
			netAdapter,
			protocolManager,
			connectionManager,
			addressManager,
			utxoIndex,
			shutDownChan,
		),
		consensusEventsChan: consensusEventsChan,
	}
	netAdapter.SetRPCRouterInitializer(manager.routerInitializer)

	manager.initConsensusEventsHandler(consensusEventsChan)

	// Start RPC statistics tracking
	RPCStats.Start()

	return &manager
}

func (m *Manager) initConsensusEventsHandler(consensusEventsChan chan externalapi.ConsensusEvent) {
	m.consensusEventsHandlerDone = make(chan struct{})
	spawn("consensusEventsHandler", func() {
		defer close(m.consensusEventsHandlerDone)
		for {
			consensusEvent, ok := <-consensusEventsChan
			if !ok {
				return
			}
			switch event := consensusEvent.(type) {
			case *externalapi.VirtualChangeSet:
				err := m.notifyVirtualChange(event)
				if err != nil {
					panic(err)
				}
			case *externalapi.BlockAdded:
				err := m.notifyBlockAddedToDAG(event.Block)
				if err != nil {
					panic(err)
				}
			case *externalapi.PruningPointUTXOSetOverride:
				event.Done <- m.notifyPruningPointUTXOSetOverride()
			default:
				panic(errors.Errorf("Got event of unsupported type %T", consensusEvent))
			}
		}
	})
}

// WaitForConsensusEventsHandler waits until the consensus events handler has exited - which it does once the events
// channel is closed and every event queued before that has been handled - or until timeout. It reports whether the
// handler exited.
//
// Shutdown closes the database right after the component manager stops. The handler writes each virtual change to the
// UTXO index, and an IBD resolve leaves many queued, so closing the database without waiting made the handler read
// from a closed database, panic, and exit the process with status 1 in the middle of the database close.
func (m *Manager) WaitForConsensusEventsHandler(timeout time.Duration) bool {
	select {
	case <-m.consensusEventsHandlerDone:
		return true
	case <-time.After(timeout):
		return false
	}
}

// notifyBlockAddedToDAG notifies the manager that a block has been added to the DAG
func (m *Manager) notifyBlockAddedToDAG(block *externalapi.DomainBlock) error {
	onEnd := logger.LogAndMeasureExecutionTime(log, "RPCManager.notifyBlockAddedToDAG")
	defer onEnd()

	// Before converting the block and populating it, we check if any listeners are interested.
	// This is done since most nodes do not use this event.
	if !m.context.NotificationManager.HasBlockAddedListeners() {
		return nil
	}

	// Block-added notifications only need header-level block data and
	// transaction IDs in verbose data. Avoid materializing the full RPC
	// transaction tree for every accepted block.
	rpcBlock := appmessage.DomainBlockToRPCBlock(&externalapi.DomainBlock{Header: block.Header})
	err := m.context.PopulateRPCBlockWithVerboseData(rpcBlock, block.Header, block, false)
	if err != nil {
		return err
	}
	blockAddedNotification := appmessage.NewBlockAddedNotificationMessage(rpcBlock)
	err = m.context.NotificationManager.NotifyBlockAdded(blockAddedNotification)
	if err != nil {
		return err
	}

	return nil
}

// notifyVirtualChange notifies the manager that the virtual block has been changed.
func (m *Manager) notifyVirtualChange(virtualChangeSet *externalapi.VirtualChangeSet) error {
	onEnd := logger.LogAndMeasureExecutionTime(log, "RPCManager.NotifyVirtualChange")
	defer onEnd()

	if m.context.Config.UTXOIndex && virtualChangeSet.VirtualUTXODiff != nil {
		err := m.notifyUTXOsChanged(virtualChangeSet)
		if err != nil {
			return err
		}
	}

	err := m.notifyVirtualSelectedParentBlueScoreChanged(virtualChangeSet.VirtualSelectedParentBlueScore)
	if err != nil {
		return err
	}

	err = m.notifyVirtualDaaScoreChanged(virtualChangeSet.VirtualDAAScore)
	if err != nil {
		return err
	}

	if virtualChangeSet.VirtualSelectedParentChainChanges == nil ||
		(len(virtualChangeSet.VirtualSelectedParentChainChanges.Added) == 0 &&
			len(virtualChangeSet.VirtualSelectedParentChainChanges.Removed) == 0) {

		return nil
	}

	err = m.notifyVirtualSelectedParentChainChanged(virtualChangeSet)
	if err != nil {
		return err
	}

	return nil
}

// NotifyNewBlockTemplate notifies the manager that a new
// block template is available for miners
func (m *Manager) NotifyNewBlockTemplate() error {
	notification := appmessage.NewNewBlockTemplateNotificationMessage()
	return m.context.NotificationManager.NotifyNewBlockTemplate(notification)
}

// NotifyPruningPointUTXOSetOverride notifies the manager whenever the UTXO index
// resets due to pruning point change via IBD.
func (m *Manager) NotifyPruningPointUTXOSetOverride() error {
	onEnd := logger.LogAndMeasureExecutionTime(log, "RPCManager.NotifyPruningPointUTXOSetOverride")
	defer onEnd()

	if m.context.Config.UTXOIndex {
		// The reset runs on the events handler, not here. Events the replaced consensus raised can
		// still be queued; had the reset run first, they would be replayed onto the rebuilt index,
		// adding coins whose removal could only have come from the consensus that was just replaced.
		// Queued behind them, the reset wipes whatever they wrote. Waiting keeps virtual still while
		// the reset pages through it, as calling Reset directly did.
		done := make(chan error, 1)
		m.consensusEventsChan <- &externalapi.PruningPointUTXOSetOverride{Done: done}
		err := <-done
		if err != nil {
			return err
		}
	}

	return nil
}

// NotifyFinalityConflict notifies the manager that there's a finality conflict in the DAG
func (m *Manager) NotifyFinalityConflict(violatingBlockHash string) error {
	onEnd := logger.LogAndMeasureExecutionTime(log, "RPCManager.NotifyFinalityConflict")
	defer onEnd()

	notification := appmessage.NewFinalityConflictNotificationMessage(violatingBlockHash)
	return m.context.NotificationManager.NotifyFinalityConflict(notification)
}

// NotifyFinalityConflictResolved notifies the manager that a finality conflict in the DAG has been resolved
func (m *Manager) NotifyFinalityConflictResolved(finalityBlockHash string) error {
	onEnd := logger.LogAndMeasureExecutionTime(log, "RPCManager.NotifyFinalityConflictResolved")
	defer onEnd()

	notification := appmessage.NewFinalityConflictResolvedNotificationMessage(finalityBlockHash)
	return m.context.NotificationManager.NotifyFinalityConflictResolved(notification)
}

func (m *Manager) notifyUTXOsChanged(virtualChangeSet *externalapi.VirtualChangeSet) error {
	onEnd := logger.LogAndMeasureExecutionTime(log, "RPCManager.NotifyUTXOsChanged")
	defer onEnd()

	utxoIndexChanges, err := m.context.UTXOIndex.Update(virtualChangeSet)
	if err != nil {
		return err
	}

	return m.context.NotificationManager.NotifyUTXOsChanged(utxoIndexChanges)
}

func (m *Manager) notifyPruningPointUTXOSetOverride() error {
	onEnd := logger.LogAndMeasureExecutionTime(log, "RPCManager.notifyPruningPointUTXOSetOverride")
	defer onEnd()

	err := m.context.UTXOIndex.Reset()
	if err != nil {
		return err
	}

	return m.context.NotificationManager.NotifyPruningPointUTXOSetOverride()
}

func (m *Manager) notifyVirtualSelectedParentBlueScoreChanged(virtualSelectedParentBlueScore uint64) error {
	onEnd := logger.LogAndMeasureExecutionTime(log, "RPCManager.NotifyVirtualSelectedParentBlueScoreChanged")
	defer onEnd()

	notification := appmessage.NewVirtualSelectedParentBlueScoreChangedNotificationMessage(virtualSelectedParentBlueScore)
	return m.context.NotificationManager.NotifyVirtualSelectedParentBlueScoreChanged(notification)
}

func (m *Manager) notifyVirtualDaaScoreChanged(virtualDAAScore uint64) error {
	onEnd := logger.LogAndMeasureExecutionTime(log, "RPCManager.NotifyVirtualDaaScoreChanged")
	defer onEnd()

	notification := appmessage.NewVirtualDaaScoreChangedNotificationMessage(virtualDAAScore)
	return m.context.NotificationManager.NotifyVirtualDaaScoreChanged(notification)
}

func (m *Manager) notifyVirtualSelectedParentChainChanged(virtualChangeSet *externalapi.VirtualChangeSet) error {
	onEnd := logger.LogAndMeasureExecutionTime(log, "RPCManager.NotifyVirtualSelectedParentChainChanged")
	defer onEnd()

	hasListeners, includeAcceptedTransactionIDs := m.context.NotificationManager.HasListenersThatPropagateVirtualSelectedParentChainChanged()

	if hasListeners {
		notification, err := m.context.ConvertVirtualSelectedParentChainChangesToChainChangedNotificationMessage(
			virtualChangeSet.VirtualSelectedParentChainChanges, includeAcceptedTransactionIDs)
		if err != nil {
			return err
		}
		return m.context.NotificationManager.NotifyVirtualSelectedParentChainChanged(notification)
	}

	return nil
}
