package domain

import (
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/HoosatNetwork/HTND/v2/domain/consensusreference"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/miningmanager"
	"github.com/HoosatNetwork/HTND/v2/domain/miningmanager/mempool"
	"github.com/HoosatNetwork/HTND/v2/domain/prefixmanager"
	"github.com/HoosatNetwork/HTND/v2/domain/prefixmanager/prefix"
	infrastructuredatabase "github.com/HoosatNetwork/HTND/v2/infrastructure/db/database"
	"github.com/pkg/errors"
)

// Domain provides a reference to the domain's external aps
type Domain interface {
	MiningManager() miningmanager.MiningManager
	Consensus() externalapi.Consensus
	StagingConsensus() externalapi.Consensus
	InitStagingConsensusWithoutGenesis() error
	CommitStagingConsensus() error
	DeleteStagingConsensus() error
	ConsensusEventsChannel() chan externalapi.ConsensusEvent
}

type domain struct {
	miningManager          miningmanager.MiningManager
	consensus              *externalapi.Consensus
	stagingConsensus       *externalapi.Consensus
	stagingConsensusLock   sync.RWMutex
	consensusConfig        *consensus.Config
	db                     infrastructuredatabase.Database
	consensusEventsChannel chan externalapi.ConsensusEvent
}

func (d *domain) ConsensusEventsChannel() chan externalapi.ConsensusEvent {
	return d.consensusEventsChannel
}

func (d *domain) Consensus() externalapi.Consensus {
	return *d.consensus
}

func (d *domain) StagingConsensus() externalapi.Consensus {
	d.stagingConsensusLock.RLock()
	defer d.stagingConsensusLock.RUnlock()
	if d.stagingConsensus == nil {
		return nil
	}
	return *d.stagingConsensus
}

func (d *domain) MiningManager() miningmanager.MiningManager {
	return d.miningManager
}

func (d *domain) InitStagingConsensusWithoutGenesis() error {
	cfg := *d.consensusConfig
	cfg.SkipAddingGenesis = true
	return d.initStagingConsensus(&cfg)
}

func (d *domain) initStagingConsensus(cfg *consensus.Config) error {
	d.stagingConsensusLock.Lock()
	defer d.stagingConsensusLock.Unlock()

	// The datadir-repair passes are one-shot recovery steps for the consensus this node has been
	// running on, but the factory runs them for every consensus it builds, and a staging consensus is
	// built fresh on each IBD attempt. They are not expensive here - the staging prefix is empty when
	// they run, so RepairBlockStatuses reports "No blocks found" in about two milliseconds - but
	// RepairMissingMultisets still re-marks a block for verification inside a consensus that IBD is in
	// the middle of constructing, which is not something a recovery flag should be doing unasked. The
	// log noise is its own problem: on mainnet 2026-09-20 a node that had not restarted once announced
	// "Starting block status repair (setting all non-invalid blocks to StatusUTXOValid)" every seven
	// minutes, which reads exactly like the destructive full-store pass that flag is named after.
	stagingCfg := *cfg
	stagingCfg.RepairBlockStatuses = false
	stagingCfg.RepairMissingMultisets = false
	cfg = &stagingCfg

	_, hasInactivePrefix, err := prefixmanager.InactivePrefix(d.db)
	if err != nil {
		return err
	}

	if hasInactivePrefix {
		return errors.Errorf("cannot create staging consensus when a staging consensus already exists")
	}

	activePrefix, exists, err := prefixmanager.ActivePrefix(d.db)
	if err != nil {
		return err
	}

	if !exists {
		return errors.Errorf("cannot create a staging consensus when there's " +
			"no active consensus")
	}

	inactivePrefix := activePrefix.Flip()
	err = prefixmanager.SetPrefixAsInactive(d.db, inactivePrefix)
	if err != nil {
		return err
	}

	consensusFactory := consensus.NewFactory()

	consensusInstance, shouldMigrate, err := consensusFactory.NewConsensus(cfg, d.db, inactivePrefix, d.consensusEventsChannel)
	if err != nil {
		return err
	}

	if shouldMigrate {
		return errors.Errorf("A fresh consensus should never return shouldMigrate=true")
	}

	d.stagingConsensus = &consensusInstance
	return nil
}

func (d *domain) CommitStagingConsensus() error {
	d.stagingConsensusLock.Lock()
	defer d.stagingConsensusLock.Unlock()

	dbTx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = dbTx.RollbackUnlessClosed() }()

	inactivePrefix, hasInactivePrefix, err := prefixmanager.InactivePrefix(d.db)
	if err != nil {
		return err
	}

	if !hasInactivePrefix {
		return errors.Errorf("there's no inactive prefix to commit")
	}

	activePrefix, exists, err := prefixmanager.ActivePrefix(dbTx)
	if err != nil {
		return err
	}

	if !exists {
		return errors.Errorf("cannot commit a staging consensus when there's " +
			"no active consensus")
	}

	err = prefixmanager.SetPrefixAsActive(dbTx, inactivePrefix)
	if err != nil {
		return err
	}

	err = prefixmanager.SetPrefixAsInactive(dbTx, activePrefix)
	if err != nil {
		return err
	}

	err = dbTx.Commit()
	if err != nil {
		return err
	}

	// The database now names the staging prefix as active, so the domain has to serve that consensus
	// from here on. The swap used to come after deleting the old prefix's data, so a failure there
	// returned with the node still running on the old consensus instance, whose prefix the database
	// had just marked inactive, while the committed instance was still held as staging.
	tempConsensusPointer := unsafe.Pointer(d.stagingConsensus)
	consensusPointer := (*unsafe.Pointer)(unsafe.Pointer(&d.consensus))
	atomic.StorePointer(consensusPointer, tempConsensusPointer)
	d.stagingConsensus = nil

	// We delete anything associated with the old prefix outside
	// of the transaction in order to save memory.
	//
	// A failure here does not fail the commit. What is left behind is marked as the inactive prefix,
	// which New deletes on the next start and InitStagingConsensusWithoutGenesis callers clear before
	// creating a new staging consensus; returning the error would make IBD skip the UTXO set override
	// for a consensus that is already in use.
	err = prefixmanager.DeleteInactivePrefix(d.db)
	if err != nil {
		log.Errorf("Committed the staging consensus, but failed to delete the previous consensus data; "+
			"it will be deleted on the next start: %s", err)
	}
	return nil
}

func (d *domain) DeleteStagingConsensus() error {
	d.stagingConsensusLock.Lock()
	defer d.stagingConsensusLock.Unlock()

	err := prefixmanager.DeleteInactivePrefix(d.db)
	if err != nil {
		return err
	}

	d.stagingConsensus = nil
	return nil
}

// New instantiates a new instance of a Domain object
func New(consensusConfig *consensus.Config, mempoolConfig *mempool.Config, db infrastructuredatabase.Database) (Domain, error) {
	err := prefixmanager.DeleteInactivePrefix(db)
	if err != nil {
		return nil, err
	}

	activePrefix, exists, err := prefixmanager.ActivePrefix(db)
	if err != nil {
		return nil, err
	}

	if !exists {
		activePrefix = &prefix.Prefix{}
		err = prefixmanager.SetPrefixAsActive(db, activePrefix)
		if err != nil {
			return nil, err
		}
	}

	consensusEventsChan := make(chan externalapi.ConsensusEvent, 100e3)
	consensusFactory := consensus.NewFactory()
	consensusInstance, shouldMigrate, err := consensusFactory.NewConsensus(consensusConfig, db, activePrefix, consensusEventsChan)
	if err != nil {
		return nil, err
	}

	domainInstance := &domain{
		consensus:              &consensusInstance,
		consensusConfig:        consensusConfig,
		db:                     db,
		consensusEventsChannel: consensusEventsChan,
	}

	if shouldMigrate {
		err := domainInstance.migrate()
		if err != nil {
			return nil, err
		}
	}

	miningManagerFactory := miningmanager.NewFactory()

	// We create a consensus wrapper because the actual consensus might change
	consensusReference := consensusreference.NewConsensusReference(&domainInstance.consensus)
	domainInstance.miningManager = miningManagerFactory.NewMiningManager(consensusReference, &consensusConfig.Params, mempoolConfig)
	return domainInstance, nil
}
