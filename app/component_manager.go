package app

import (
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"

	"github.com/HoosatNetwork/HTND/domain/miningmanager/mempool"

	"github.com/HoosatNetwork/HTND/app/protocol"
	"github.com/HoosatNetwork/HTND/app/rpc"
	"github.com/HoosatNetwork/HTND/domain"
	"github.com/HoosatNetwork/HTND/domain/consensus"
	"github.com/HoosatNetwork/HTND/domain/utxoindex"
	"github.com/HoosatNetwork/HTND/infrastructure/autoupdate"
	"github.com/HoosatNetwork/HTND/infrastructure/config"
	infrastructuredatabase "github.com/HoosatNetwork/HTND/infrastructure/db/database"
	"github.com/HoosatNetwork/HTND/infrastructure/network/addressmanager"
	"github.com/HoosatNetwork/HTND/infrastructure/network/connmanager"
	"github.com/HoosatNetwork/HTND/infrastructure/network/netadapter"
	"github.com/HoosatNetwork/HTND/infrastructure/network/netadapter/id"
	"github.com/HoosatNetwork/HTND/util/panics"
)

func checkedDurationFromHours(value uint64) (time.Duration, error) {
	parsedValue, err := strconv.ParseInt(strconv.FormatUint(value, 10), 10, 64)
	if err != nil {
		return 0, err
	}
	return time.Duration(parsedValue) * time.Hour, nil
}

// ComponentManager is a wrapper for all the htnd services
type ComponentManager struct {
	cfg               *config.Config
	addressManager    *addressmanager.AddressManager
	protocolManager   *protocol.Manager
	rpcManager        *rpc.Manager
	connectionManager *connmanager.ConnectionManager
	netAdapter        *netadapter.NetAdapter
	updater           *autoupdate.Updater

	started, shutdown int32
}

// Start launches all the htnd services.
func (a *ComponentManager) Start() {
	// Already started?
	if atomic.AddInt32(&a.started, 1) != 1 {
		return
	}

	log.Trace("Starting htnd")

	err := a.netAdapter.Start()
	if err != nil {
		panics.Exit(log, fmt.Sprintf("Error starting the net adapter: %+v", err))
	}

	a.connectionManager.Start()

	// Start the auto-updater
	if a.updater != nil {
		a.updater.Start()
	}
}

// Stop gracefully shuts down all the htnd services.
func (a *ComponentManager) Stop() {
	// Make sure this only happens once.
	if atomic.AddInt32(&a.shutdown, 1) != 1 {
		log.Infof("htnd is already in the process of shutting down")
		return
	}

	log.Warnf("htnd shutting down")

	log.Infof("Signaling protocol shutdown to all active flows")
	a.protocolManager.SignalShutdown()

	log.Infof("Stopping auto-updater")
	if a.updater != nil {
		a.updater.Stop()
		log.Infof("Auto-updater stopped")
	} else {
		log.Infof("Auto-updater not enabled")
	}

	log.Infof("Stopping RPC statistics tracking")
	rpc.RPCStats.Stop()
	log.Infof("RPC statistics tracking stopped")

	type stopResult struct {
		component string
		err       error
	}
	results := make(chan stopResult, 2)
	var waitGroup sync.WaitGroup
	waitGroup.Add(2)

	go func() {
		defer waitGroup.Done()
		log.Infof("Stopping connection manager (force-disconnecting peers)")
		a.connectionManager.Stop()
		results <- stopResult{component: "connection manager"}
	}()

	go func() {
		defer waitGroup.Done()
		log.Infof("Stopping net adapter (closing P2P/RPC servers)")
		err := a.netAdapter.Stop()
		results <- stopResult{component: "net adapter", err: err}
	}()

	waitGroup.Wait()
	close(results)
	for result := range results {
		if result.err != nil {
			log.Errorf("Error stopping the %s: %+v", result.component, result.err)
			continue
		}
		log.Infof("%s stopped", result.component)
	}

	log.Infof("Waiting for protocol flows to stop")
	a.protocolManager.Close()
	log.Infof("Protocol flows stopped")

	log.Infof("Closing consensus event channel")
	close(a.protocolManager.Context().Domain().ConsensusEventsChannel())
	log.Infof("Consensus event channel closed")
}

// NewComponentManager returns a new ComponentManager instance.
// Use Start() to begin all services within this ComponentManager
func NewComponentManager(cfg *config.Config, db infrastructuredatabase.Database, interrupt chan<- struct{}) (
	*ComponentManager, error,
) {
	dataRetentionDuration, err := checkedDurationFromHours(cfg.DataRetentionHours)
	if err != nil {
		return nil, err
	}
	pruningInterval, err := checkedDurationFromHours(cfg.PruningIntervalHours)
	if err != nil {
		return nil, err
	}
	consensusConfig := consensus.Config{
		Params:                            *cfg.ActiveNetParams,
		IsArchival:                        cfg.IsArchivalNode,
		DeletionDepth:                     cfg.DeletionDepth,
		DataRetentionDuration:             dataRetentionDuration,
		PruningInterval:                   pruningInterval,
		EnableSanityCheckPruningUTXOSet:   cfg.EnableSanityCheckPruningUTXOSet,
		EnableUTXODebugDiagnostics:        cfg.EnableUTXODebugDiagnostics,
		EnableAutoExodusExportOnPruning:   cfg.EnableAutoExodusExportOnPruning,
		AutoExodusExportDir:               cfg.AutoExodusExportDir,
		UseHoohashCLibrary:                cfg.UseHoohashCLibrary,
		PastMedianTimeValidationTolerance: cfg.PastMedianTimeValidationTolerance,
	}
	mempoolConfig := mempool.DefaultConfig(&consensusConfig.Params)
	mempoolConfig.MaximumOrphanTransactionCount = cfg.MaxOrphanTxs
	mempoolConfig.MinimumRelayTransactionFee = cfg.MinRelayTxFee

	// Configure compound transaction rate limiting (always enabled)
	mempoolConfig.CompoundTxRateLimitEnabled = true
	if cfg.MaxCompoundTxPerMinute > 0 {
		mempoolConfig.MaxCompoundTxPerAddressPerMinute = cfg.MaxCompoundTxPerMinute
	}
	if cfg.CompoundTxRateLimitWindow > 0 {
		mempoolConfig.CompoundTxRateLimitWindowMinutes = cfg.CompoundTxRateLimitWindow
	}
	if cfg.CompoundTxInputsThreshold > 0 {
		mempoolConfig.CompoundTxMinInputsThreshold = cfg.CompoundTxInputsThreshold
	}

	// Configure wallet freezing (always enabled)
	mempoolConfig.WalletFreezingEnabled = true
	if len(cfg.FrozenAddresses) > 0 {
		mempoolConfig.FrozenAddresses = cfg.FrozenAddresses
	}

	domain, err := domain.New(&consensusConfig, mempoolConfig, db)
	if err != nil {
		return nil, err
	}

	netAdapter, err := netadapter.NewNetAdapter(cfg)
	if err != nil {
		return nil, err
	}

	addressManager, err := addressmanager.New(addressmanager.NewConfig(cfg), db)
	if err != nil {
		return nil, err
	}

	var utxoIndex *utxoindex.UTXOIndex
	if cfg.UTXOIndex {
		utxoIndex, err = utxoindex.New(domain, db)
		if err != nil {
			return nil, err
		}

		log.Infof("UTXO index started")
	}

	connectionManager, err := connmanager.New(cfg, netAdapter, addressManager)
	if err != nil {
		return nil, err
	}
	protocolManager, err := protocol.NewManager(cfg, domain, netAdapter, addressManager, connectionManager)
	if err != nil {
		return nil, err
	}
	rpcManager := setupRPC(cfg, domain, netAdapter, protocolManager, connectionManager, addressManager, utxoIndex, domain.ConsensusEventsChannel(), interrupt)

	// Create auto-updater if enabled
	var updater *autoupdate.Updater
	if bool(cfg.AutoUpdateEnabled) {
		updaterConfig := &autoupdate.Config{
			Enabled:          bool(cfg.AutoUpdateEnabled),
			CheckInterval:    cfg.AutoUpdateCheckInterval,
			GitHubOwner:      "HoosatNetwork",
			GitHubRepo:       "HTND",
			UpdateChannel:    cfg.AutoUpdateChannel,
			AutoDownload:     bool(cfg.AutoUpdateDownload),
			AutoInstall:      bool(cfg.AutoUpdateInstall),
			NotifyOnly:       false,
			AutoReportIssues: bool(cfg.AutoReportIssues),
		}
		updater = autoupdate.NewUpdater(updaterConfig)
		log.Infof("Auto-updater initialized (channel: %s, interval: %v, autoreport: %v)", updaterConfig.UpdateChannel, updaterConfig.CheckInterval, updaterConfig.AutoReportIssues)
	}

	return &ComponentManager{
		cfg:               cfg,
		protocolManager:   protocolManager,
		rpcManager:        rpcManager,
		connectionManager: connectionManager,
		netAdapter:        netAdapter,
		addressManager:    addressManager,
		updater:           updater,
	}, nil
}

func setupRPC(
	cfg *config.Config,
	domain domain.Domain,
	netAdapter *netadapter.NetAdapter,
	protocolManager *protocol.Manager,
	connectionManager *connmanager.ConnectionManager,
	addressManager *addressmanager.AddressManager,
	utxoIndex *utxoindex.UTXOIndex,
	consensusEventsChan chan externalapi.ConsensusEvent,
	shutDownChan chan<- struct{},
) *rpc.Manager {
	rpcManager := rpc.NewManager(
		cfg,
		domain,
		netAdapter,
		protocolManager,
		connectionManager,
		addressManager,
		utxoIndex,
		consensusEventsChan,
		shutDownChan,
	)
	protocolManager.SetOnNewBlockTemplateHandler(rpcManager.NotifyNewBlockTemplate)
	protocolManager.SetOnPruningPointUTXOSetOverrideHandler(rpcManager.NotifyPruningPointUTXOSetOverride)

	return rpcManager
}

// P2PNodeID returns the network ID associated with this ComponentManager
func (a *ComponentManager) P2PNodeID() *id.ID {
	return a.netAdapter.ID()
}

// AddressManager returns the AddressManager associated with this ComponentManager
func (a *ComponentManager) AddressManager() *addressmanager.AddressManager {
	return a.addressManager
}

// Updater returns the Updater associated with this ComponentManager, or nil if auto-update is disabled
func (a *ComponentManager) Updater() *autoupdate.Updater {
	return a.updater
}
