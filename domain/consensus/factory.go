package consensus

import (
	"os"
	"strconv"
	"sync"

	"github.com/Hoosat-Oy/HTND/domain/consensus/datastructures/blockwindowheapslicestore"
	"github.com/Hoosat-Oy/HTND/domain/consensus/datastructures/daawindowstore"
	"github.com/Hoosat-Oy/HTND/domain/consensus/datastructures/mergedepthrootstore"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model"
	"github.com/Hoosat-Oy/HTND/domain/consensus/processes/blockparentbuilder"
	parentssanager "github.com/Hoosat-Oy/HTND/domain/consensus/processes/parentsmanager"
	"github.com/Hoosat-Oy/HTND/domain/consensus/processes/pruningproofmanager"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/constants"
	"github.com/Hoosat-Oy/HTND/util/staging"
	"github.com/pkg/errors"

	"github.com/Hoosat-Oy/HTND/domain/prefixmanager/prefix"
	"github.com/Hoosat-Oy/HTND/util/txmass"

	consensusdatabase "github.com/Hoosat-Oy/HTND/domain/consensus/database"
	"github.com/Hoosat-Oy/HTND/domain/consensus/datastructures/acceptancedatastore"
	"github.com/Hoosat-Oy/HTND/domain/consensus/datastructures/blockheaderstore"
	"github.com/Hoosat-Oy/HTND/domain/consensus/datastructures/blockrelationstore"
	"github.com/Hoosat-Oy/HTND/domain/consensus/datastructures/blockstatusstore"
	"github.com/Hoosat-Oy/HTND/domain/consensus/datastructures/blockstore"
	"github.com/Hoosat-Oy/HTND/domain/consensus/datastructures/consensusstatestore"
	"github.com/Hoosat-Oy/HTND/domain/consensus/datastructures/daablocksstore"
	"github.com/Hoosat-Oy/HTND/domain/consensus/datastructures/finalitystore"
	"github.com/Hoosat-Oy/HTND/domain/consensus/datastructures/ghostdagdatastore"
	"github.com/Hoosat-Oy/HTND/domain/consensus/datastructures/headersselectedchainstore"
	"github.com/Hoosat-Oy/HTND/domain/consensus/datastructures/headersselectedtipstore"
	"github.com/Hoosat-Oy/HTND/domain/consensus/datastructures/multisetstore"
	"github.com/Hoosat-Oy/HTND/domain/consensus/datastructures/pruningstore"
	"github.com/Hoosat-Oy/HTND/domain/consensus/datastructures/reachabilitydatastore"
	"github.com/Hoosat-Oy/HTND/domain/consensus/datastructures/utxodiffstore"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/testapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/processes/blockbuilder"
	"github.com/Hoosat-Oy/HTND/domain/consensus/processes/blockprocessor"
	"github.com/Hoosat-Oy/HTND/domain/consensus/processes/blockvalidator"
	"github.com/Hoosat-Oy/HTND/domain/consensus/processes/coinbasemanager"
	"github.com/Hoosat-Oy/HTND/domain/consensus/processes/consensusstatemanager"
	"github.com/Hoosat-Oy/HTND/domain/consensus/processes/dagtopologymanager"
	"github.com/Hoosat-Oy/HTND/domain/consensus/processes/dagtraversalmanager"
	"github.com/Hoosat-Oy/HTND/domain/consensus/processes/difficultymanager"
	"github.com/Hoosat-Oy/HTND/domain/consensus/processes/finalitymanager"
	"github.com/Hoosat-Oy/HTND/domain/consensus/processes/ghostdagmanager"
	"github.com/Hoosat-Oy/HTND/domain/consensus/processes/headersselectedtipmanager"
	"github.com/Hoosat-Oy/HTND/domain/consensus/processes/mergedepthmanager"
	"github.com/Hoosat-Oy/HTND/domain/consensus/processes/pastmediantimemanager"
	"github.com/Hoosat-Oy/HTND/domain/consensus/processes/pruningmanager"
	"github.com/Hoosat-Oy/HTND/domain/consensus/processes/reachabilitymanager"
	"github.com/Hoosat-Oy/HTND/domain/consensus/processes/syncmanager"
	"github.com/Hoosat-Oy/HTND/domain/consensus/processes/transactionvalidator"
	"github.com/Hoosat-Oy/HTND/domain/dagconfig"
	infrastructuredatabase "github.com/Hoosat-Oy/HTND/infrastructure/db/database"
	"github.com/Hoosat-Oy/HTND/infrastructure/db/database/ldb"
)

const (
	defaultTestLeveldbCacheSizeMiB = 8
	defaultPreallocateCaches       = true
	defaultTestPreallocateCaches   = false
)

// Config is the full config required to run consensus
type Config struct {
	dagconfig.Params
	// IsArchival tells the consensus if it should not prune old blocks
	IsArchival bool
	// EnableSanityCheckPruningUTXOSet checks the full pruning point utxo set against the commitment at every pruning movement
	EnableSanityCheckPruningUTXOSet bool

	SkipAddingGenesis bool

	// DeletionDepth specifies the depth for block deletion during pruning
	DeletionDepth uint64
}

// Factory instantiates new Consensuses
type Factory interface {
	NewConsensus(config *Config, db infrastructuredatabase.Database, dbPrefix *prefix.Prefix,
		consensusEventsChan chan externalapi.ConsensusEvent) (
		externalapi.Consensus, bool, error)
	NewTestConsensus(config *Config, testName string) (
		tc testapi.TestConsensus, teardown func(keepDataDir bool), err error)

	SetTestDataDir(dataDir string)
	SetTestGHOSTDAGManager(ghostdagConstructor GHOSTDAGManagerConstructor)
	SetTestLevelDBCacheSize(cacheSizeMiB int)
	SetTestPreAllocateCache(preallocateCaches bool)
	SetTestPastMedianTimeManager(medianTimeConstructor PastMedianTimeManagerConstructor)
	SetTestDifficultyManager(difficultyConstructor DifficultyManagerConstructor)
}

type factory struct {
	dataDir                  string
	ghostdagConstructor      GHOSTDAGManagerConstructor
	pastMedianTimeConsructor PastMedianTimeManagerConstructor
	difficultyConstructor    DifficultyManagerConstructor
	cacheSizeMiB             *int
	preallocateCaches        *bool
}

// NewFactory creates a new Consensus factory
func NewFactory() Factory {
	return &factory{
		ghostdagConstructor:      ghostdagmanager.New,
		pastMedianTimeConsructor: pastmediantimemanager.New,
		difficultyConstructor:    difficultymanager.New,
	}
}

// NewConsensus instantiates a new Consensus
func (f *factory) NewConsensus(config *Config, db infrastructuredatabase.Database, dbPrefix *prefix.Prefix,
	consensusEventsChan chan externalapi.ConsensusEvent) (
	consensusInstance externalapi.Consensus, shouldMigrate bool, err error) {

	dbManager := consensusdatabase.New(db)
	prefixBucket := consensusdatabase.MakeBucket(dbPrefix.Serialize())

	var largeCacheDivisor int = 1
	if v := os.Getenv("HTND_LARGE_CACHE_DIVISOR"); v != "" {
		if divisor, err := strconv.Atoi(v); err == nil && divisor > 0 {
			if divisor > 50 {
				divisor = 50
			}
			largeCacheDivisor = divisor << 20
		}
	}

	pruningWindowSizeForCaches := int(config.PruningDepth()) / largeCacheDivisor
	finalityWindowSizeForCaches := int(config.FinalityDepth()) / largeCacheDivisor
	// This is used for caches that are used as part of deletePastBlocks that need to traverse until
	// the previous pruning point.
	pruningWindowSizePlusFinalityDepthForCache := int(pruningWindowSizeForCaches + finalityWindowSizeForCaches)
	log.Infof("Largest cache sizes %d, %d, %d", finalityWindowSizeForCaches, pruningWindowSizeForCaches, pruningWindowSizePlusFinalityDepthForCache)

	var preallocateCaches bool
	if f.preallocateCaches != nil {
		preallocateCaches = *f.preallocateCaches
	} else {
		preallocateCaches = defaultPreallocateCaches
	}

	// Data Structures
	mergeDepthRootStore := mergedepthrootstore.New(prefixBucket, 1000, preallocateCaches)
	daaWindowStore := daawindowstore.New(prefixBucket, 1000, preallocateCaches)
	acceptanceDataStore := acceptancedatastore.New(prefixBucket, 1000, preallocateCaches)
	blockStore, err := blockstore.New(dbManager, prefixBucket, 10_000, preallocateCaches)
	if err != nil {
		return nil, false, err
	}
	blockHeaderStore, err := blockheaderstore.New(dbManager, prefixBucket, 10_000, preallocateCaches)
	if err != nil {
		return nil, false, err
	}

	blockStatusStore := blockstatusstore.New(prefixBucket, pruningWindowSizePlusFinalityDepthForCache, preallocateCaches)
	multisetStore := multisetstore.New(prefixBucket, 1000, preallocateCaches)
	pruningStore := pruningstore.New(prefixBucket, 2, preallocateCaches)
	utxoDiffStore := utxodiffstore.New(prefixBucket, 1000, preallocateCaches)
	consensusStateStore := consensusstatestore.New(prefixBucket, 1000, preallocateCaches)

	headersSelectedTipStore := headersselectedtipstore.New(prefixBucket)
	finalityStore := finalitystore.New(prefixBucket, 1000, preallocateCaches)
	headersSelectedChainStore := headersselectedchainstore.New(prefixBucket, pruningWindowSizeForCaches, preallocateCaches)
	daaBlocksStore := daablocksstore.New(prefixBucket, pruningWindowSizeForCaches, finalityWindowSizeForCaches, preallocateCaches)
	windowHeapSliceStore := blockwindowheapslicestore.New(10_000, preallocateCaches)

	newReachabilityDataStore := reachabilitydatastore.New(prefixBucket, pruningWindowSizePlusFinalityDepthForCache*2, preallocateCaches)
	blockRelationStores, reachabilityDataStores, ghostdagDataStores := dagStores(config, prefixBucket, pruningWindowSizePlusFinalityDepthForCache, pruningWindowSizeForCaches, preallocateCaches)
	oldReachabilityManager := reachabilitymanager.New(
		dbManager,
		ghostdagDataStores[0],
		reachabilityDataStores[0])
	isOldReachabilityInitialized, err := reachabilityDataStores[0].HasReachabilityData(dbManager, model.NewStagingArea(), model.VirtualGenesisBlockHash)
	if err != nil {
		return nil, false, err
	}

	newReachabilityManager := reachabilitymanager.New(
		dbManager,
		ghostdagDataStores[0],
		newReachabilityDataStore)
	reachabilityManager := newReachabilityManager
	if isOldReachabilityInitialized {
		reachabilityManager = oldReachabilityManager
	} else {
		for i := range reachabilityDataStores {
			reachabilityDataStores[i] = newReachabilityDataStore
		}
	}
	reachabilityDataStore := reachabilityDataStores[0]

	dagTopologyManagers, ghostdagManagers, dagTraversalManagers := f.dagProcesses(config, dbManager, blockHeaderStore, daaWindowStore, windowHeapSliceStore, blockRelationStores, reachabilityDataStores, ghostdagDataStores, isOldReachabilityInitialized)

	blockRelationStore := blockRelationStores[0]

	ghostdagDataStore := ghostdagDataStores[0]

	dagTopologyManager := dagTopologyManagers[0]
	ghostdagManager := ghostdagManagers[0]
	dagTraversalManager := dagTraversalManagers[0]

	// Processes
	parentsManager := parentssanager.New(config.GenesisHash, config.MaxBlockLevel)
	blockParentBuilder := blockparentbuilder.New(
		dbManager,
		blockHeaderStore,
		dagTopologyManager,
		parentsManager,
		reachabilityDataStore,
		pruningStore,

		config.GenesisHash,
		config.MaxBlockLevel,
	)

	txMassCalculator := txmass.NewCalculator(config.MassPerTxByte, config.MassPerScriptPubKeyByte, config.MassPerSigOp)

	pastMedianTimeManager := f.pastMedianTimeConsructor(
		config.TimestampDeviationTolerance,
		dbManager,
		dagTraversalManager,
		blockHeaderStore,
		ghostdagDataStore,
		config.GenesisHash)
	transactionValidator := transactionvalidator.New(config.BlockCoinbaseMaturity,
		config.EnableNonNativeSubnetworks,
		config.MaxCoinbasePayloadLength,
		config.MergeSetSizeLimit,
		config.CoinbasePayloadScriptPublicKeyMaxLength,
		dbManager,
		pastMedianTimeManager,
		ghostdagDataStore,
		daaBlocksStore,
		txMassCalculator)
	difficultyManager := f.difficultyConstructor(
		dbManager,
		ghostdagManager,
		ghostdagDataStore,
		blockHeaderStore,
		daaBlocksStore,
		dagTopologyManager,
		dagTraversalManager,
		config.PowMax,
		config.DifficultyAdjustmentWindowSize,
		config.DisableDifficultyAdjustment,
		config.TargetTimePerBlock,
		config.GenesisHash,
		config.GenesisBlock.Header.Bits())
	coinbaseManager := coinbasemanager.New(
		dbManager,
		config.SubsidyGenesisReward,
		config.PreDeflationaryPhaseBaseSubsidy,
		config.CoinbasePayloadScriptPublicKeyMaxLength,
		config.GenesisHash,
		config.DeflationaryPhaseDaaScore,
		config.DeflationaryPhaseBaseSubsidy,
		config.DeflationaryPhaseCurveFactor,
		config.TargetTimePerBlock,

		dagTraversalManager,
		ghostdagDataStore,
		acceptanceDataStore,
		daaBlocksStore,
		blockStore,
		pruningStore,
		blockHeaderStore)
	headerTipsManager := headersselectedtipmanager.New(dbManager, dagTopologyManager, dagTraversalManager,
		ghostdagManager, headersSelectedTipStore, headersSelectedChainStore)
	genesisHash := config.GenesisHash
	finalityManager := finalitymanager.New(
		dbManager,
		dagTopologyManager,
		finalityStore,
		ghostdagDataStore,
		pruningStore,
		genesisHash,
		config.FinalityDepth())
	mergeDepthManager := mergedepthmanager.New(
		dbManager,
		dagTopologyManager,
		dagTraversalManager,
		finalityManager,
		genesisHash,
		config.MergeDepth,
		ghostdagDataStore,
		mergeDepthRootStore,
		daaBlocksStore,
		pruningStore,
		finalityStore)
	consensusStateManager, err := consensusstatemanager.New(
		dbManager,
		config.MaxBlockParents,
		config.MergeSetSizeLimit,
		genesisHash,

		ghostdagManager,
		dagTopologyManager,
		dagTraversalManager,
		pastMedianTimeManager,
		transactionValidator,
		coinbaseManager,
		mergeDepthManager,
		finalityManager,
		difficultyManager,

		blockStatusStore,
		ghostdagDataStore,
		consensusStateStore,
		multisetStore,
		blockStore,
		utxoDiffStore,
		blockRelationStore,
		acceptanceDataStore,
		blockHeaderStore,
		headersSelectedTipStore,
		pruningStore,
		daaBlocksStore,
		finalityStore,
		headersSelectedChainStore,
		mergeDepthRootStore,
		windowHeapSliceStore)
	if err != nil {
		return nil, false, err
	}

	pruningManager := pruningmanager.New(
		dbManager,
		dagTraversalManager,
		dagTopologyManager,
		consensusStateManager,
		finalityManager,

		consensusStateStore,
		ghostdagDataStore,
		pruningStore,
		blockStatusStore,
		headersSelectedTipStore,
		multisetStore,
		acceptanceDataStore,
		blockStore,
		blockHeaderStore,
		utxoDiffStore,
		daaBlocksStore,
		reachabilityDataStore,
		daaWindowStore,

		config.IsArchival,
		genesisHash,
		config.FinalityDepth(),
		config.PruningDepth(),
		config.DeletionDepth,
		config.EnableSanityCheckPruningUTXOSet,
		config.K,
		config.DifficultyAdjustmentWindowSize,
		config.TargetTimePerBlock,
	)

	blockValidator := blockvalidator.New(
		config.PowMax,
		config.SkipProofOfWork,
		genesisHash,
		config.EnableNonNativeSubnetworks,
		config.MaxBlockMass,
		config.MergeSetSizeLimit,
		config.MaxBlockParents,
		config.TimestampDeviationTolerance,
		config.TargetTimePerBlock,
		config.POWScores,
		config.MaxBlockLevel,

		dbManager,
		difficultyManager,
		pastMedianTimeManager,
		transactionValidator,
		ghostdagManagers,
		dagTopologyManagers,
		dagTraversalManager,
		coinbaseManager,
		mergeDepthManager,
		reachabilityManager,
		finalityManager,
		blockParentBuilder,
		pruningManager,
		parentsManager,

		pruningStore,
		blockStore,
		ghostdagDataStores,
		blockHeaderStore,
		blockStatusStore,
		reachabilityDataStore,
		consensusStateStore,
		daaBlocksStore,

		txMassCalculator,
	)

	syncManager := syncmanager.New(
		dbManager,
		genesisHash,
		config.MergeSetSizeLimit,
		dagTraversalManager,
		dagTopologyManager,
		ghostdagManager,
		pruningManager,

		ghostdagDataStore,
		blockStatusStore,
		blockHeaderStore,
		blockStore,
		pruningStore,
		headersSelectedChainStore)

	blockBuilder := blockbuilder.New(
		dbManager,
		genesisHash,
		config.POWScores,

		difficultyManager,
		pastMedianTimeManager,
		coinbaseManager,
		consensusStateManager,
		ghostdagManager,
		transactionValidator,
		finalityManager,
		blockParentBuilder,
		pruningManager,

		acceptanceDataStore,
		blockRelationStore,
		multisetStore,
		ghostdagDataStore,
		daaBlocksStore,
	)

	blockProcessor := blockprocessor.New(
		genesisHash,
		config.TargetTimePerBlock,
		config.MaxBlockLevel,
		dbManager,
		consensusStateManager,
		pruningManager,
		blockValidator,
		dagTopologyManager,
		reachabilityManager,
		difficultyManager,
		pastMedianTimeManager,
		coinbaseManager,
		headerTipsManager,
		syncManager,

		acceptanceDataStore,
		blockStore,
		blockStatusStore,
		blockRelationStore,
		multisetStore,
		ghostdagDataStore,
		consensusStateStore,
		pruningStore,
		reachabilityDataStore,
		utxoDiffStore,
		blockHeaderStore,
		headersSelectedTipStore,
		finalityStore,
		headersSelectedChainStore,
		daaBlocksStore,
		daaWindowStore)

	pruningProofManager := pruningproofmanager.New(
		dbManager,
		dagTopologyManagers,
		ghostdagManagers,
		reachabilityManager,
		dagTraversalManagers,
		parentsManager,
		pruningManager,

		ghostdagDataStores,
		pruningStore,
		blockHeaderStore,
		blockStatusStore,
		finalityStore,
		consensusStateStore,
		blockRelationStore,
		reachabilityDataStore,

		genesisHash,
		config.K,
		config.PruningProofM,
		config.MaxBlockLevel,
	)

	c := &consensus{
		lock:            &sync.Mutex{},
		databaseContext: dbManager,

		genesisBlock: config.GenesisBlock,
		genesisHash:  config.GenesisHash,

		expectedDAAWindowDurationInMilliseconds: config.TargetTimePerBlock[constants.GetBlockVersion()-1].Milliseconds() * int64(config.DifficultyAdjustmentWindowSize[constants.GetBlockVersion()-1]),

		blockProcessor:        blockProcessor,
		blockBuilder:          blockBuilder,
		consensusStateManager: consensusStateManager,
		transactionValidator:  transactionValidator,
		syncManager:           syncManager,
		pastMedianTimeManager: pastMedianTimeManager,
		blockValidator:        blockValidator,
		coinbaseManager:       coinbaseManager,
		dagTopologyManagers:   dagTopologyManagers,
		dagTraversalManager:   dagTraversalManager,
		difficultyManager:     difficultyManager,
		ghostdagManagers:      ghostdagManagers,
		headerTipsManager:     headerTipsManager,
		mergeDepthManager:     mergeDepthManager,
		pruningManager:        pruningManager,
		reachabilityManager:   reachabilityManager,
		finalityManager:       finalityManager,
		pruningProofManager:   pruningProofManager,

		acceptanceDataStore:                 acceptanceDataStore,
		blockStore:                          blockStore,
		blockHeaderStore:                    blockHeaderStore,
		pruningStore:                        pruningStore,
		ghostdagDataStores:                  ghostdagDataStores,
		blockStatusStore:                    blockStatusStore,
		blockRelationStores:                 blockRelationStores,
		consensusStateStore:                 consensusStateStore,
		headersSelectedTipStore:             headersSelectedTipStore,
		multisetStore:                       multisetStore,
		reachabilityDataStore:               reachabilityDataStore,
		utxoDiffStore:                       utxoDiffStore,
		finalityStore:                       finalityStore,
		headersSelectedChainStore:           headersSelectedChainStore,
		daaBlocksStore:                      daaBlocksStore,
		blocksWithTrustedDataDAAWindowStore: daaWindowStore,
		windowHeapSliceStore:                windowHeapSliceStore,

		consensusEventsChan: consensusEventsChan,
		virtualNotUpdated:   true,
	}

	if isOldReachabilityInitialized {
		return c, true, nil
	}

	err = c.Init(config.SkipAddingGenesis)
	if err != nil {
		return nil, false, err
	}

	err = consensusStateManager.RecoverUTXOIfRequired()
	if err != nil {
		return nil, false, err
	}
	err = pruningManager.ClearImportedPruningPointData()
	if err != nil {
		return nil, false, err
	}
	err = pruningManager.UpdatePruningPointIfRequired()
	if err != nil {
		return nil, false, err
	}

	// If the virtual moved before shutdown but the pruning point hasn't, we
	// move it if needed.
	stagingArea := model.NewStagingArea()
	err = pruningManager.UpdatePruningPointByVirtual(stagingArea)
	if err != nil {
		return nil, false, err
	}

	err = staging.CommitAllChanges(dbManager, stagingArea)
	if err != nil {
		return nil, false, err
	}

	err = pruningManager.UpdatePruningPointIfRequired()
	if err != nil {
		return nil, false, err
	}

	return c, false, nil
}

func (f *factory) NewTestConsensus(config *Config, testName string) (
	tc testapi.TestConsensus, teardown func(keepDataDir bool), err error) {
	datadir := f.dataDir
	if datadir == "" {
		datadir, err = os.MkdirTemp("", testName)
		if err != nil {
			return nil, nil, err
		}
	}
	var cacheSizeMiB int
	if f.cacheSizeMiB != nil {
		cacheSizeMiB = *f.cacheSizeMiB
	} else {
		cacheSizeMiB = defaultTestLeveldbCacheSizeMiB
	}
	if f.preallocateCaches == nil {
		f.SetTestPreAllocateCache(defaultTestPreallocateCaches)
	}
	db, err := ldb.NewLevelDB(datadir, cacheSizeMiB)
	if err != nil {
		return nil, nil, err
	}

	testConsensusDBPrefix := &prefix.Prefix{}
	constants.ForceSetBlockVersion(1)
	consensusAsInterface, shouldMigrate, err := f.NewConsensus(config, db, testConsensusDBPrefix, nil)
	if err != nil {
		return nil, nil, err
	}
	constants.ForceSetBlockVersion(1)

	if shouldMigrate {
		return nil, nil, errors.Errorf("A fresh consensus should never return shouldMigrate=true")
	}

	consensusAsImplementation := consensusAsInterface.(*consensus)
	testConsensusStateManager := consensusstatemanager.NewTestConsensusStateManager(consensusAsImplementation.consensusStateManager)
	testTransactionValidator := transactionvalidator.NewTestTransactionValidator(consensusAsImplementation.transactionValidator)

	tstConsensus := &testConsensus{
		dagParams:                 &config.Params,
		consensus:                 consensusAsImplementation,
		database:                  db,
		testConsensusStateManager: testConsensusStateManager,
		testReachabilityManager: reachabilitymanager.NewTestReachabilityManager(consensusAsImplementation.
			reachabilityManager),
		testTransactionValidator: testTransactionValidator,
	}
	tstConsensus.testBlockBuilder = blockbuilder.NewTestBlockBuilder(consensusAsImplementation.blockBuilder, tstConsensus)
	teardown = func(keepDataDir bool) {
		db.Close()
		if !keepDataDir {
			err := os.RemoveAll(datadir)
			if err != nil {
				log.Errorf("Error removing data directory for test consensus: %s", err)
			}
		}
	}
	return tstConsensus, teardown, nil
}

func (f *factory) SetTestDataDir(dataDir string) {
	f.dataDir = dataDir
}

func (f *factory) SetTestGHOSTDAGManager(ghostdagConstructor GHOSTDAGManagerConstructor) {
	f.ghostdagConstructor = ghostdagConstructor
}

func (f *factory) SetTestPastMedianTimeManager(medianTimeConstructor PastMedianTimeManagerConstructor) {
	f.pastMedianTimeConsructor = medianTimeConstructor
}

// SetTestDifficultyManager is a setter for the difficultyManager field on the factory.
func (f *factory) SetTestDifficultyManager(difficultyConstructor DifficultyManagerConstructor) {
	f.difficultyConstructor = difficultyConstructor
}

func (f *factory) SetTestLevelDBCacheSize(cacheSizeMiB int) {
	f.cacheSizeMiB = &cacheSizeMiB
}
func (f *factory) SetTestPreAllocateCache(preallocateCaches bool) {
	f.preallocateCaches = &preallocateCaches
}

func dagStores(config *Config,
	prefixBucket model.DBBucket,
	pruningWindowSizePlusFinalityDepthForCache, pruningWindowSizeForCaches int,
	preallocateCaches bool) ([]model.BlockRelationStore, []model.ReachabilityDataStore, []model.GHOSTDAGDataStore) {

	blockRelationStores := make([]model.BlockRelationStore, config.MaxBlockLevel+1)
	reachabilityDataStores := make([]model.ReachabilityDataStore, config.MaxBlockLevel+1)
	ghostdagDataStores := make([]model.GHOSTDAGDataStore, config.MaxBlockLevel+1)

	ghostdagDataCacheSize := max(pruningWindowSizeForCaches*2, config.DifficultyAdjustmentWindowSize[constants.GetBlockVersion()-1])

	for i := 0; i <= config.MaxBlockLevel; i++ {
		prefixBucket := prefixBucket.Bucket([]byte{byte(i)})
		if i == 0 {
			blockRelationStores[i] = blockrelationstore.New(prefixBucket, pruningWindowSizePlusFinalityDepthForCache, preallocateCaches)
			reachabilityDataStores[i] = reachabilitydatastore.New(prefixBucket, pruningWindowSizePlusFinalityDepthForCache*2, preallocateCaches)
			ghostdagDataStores[i] = ghostdagdatastore.New(prefixBucket, ghostdagDataCacheSize, preallocateCaches)
		} else {
			blockRelationStores[i] = blockrelationstore.New(prefixBucket, 200, false)
			reachabilityDataStores[i] = reachabilitydatastore.New(prefixBucket, pruningWindowSizePlusFinalityDepthForCache, false)
			ghostdagDataStores[i] = ghostdagdatastore.New(prefixBucket, 200, false)
		}
	}

	return blockRelationStores, reachabilityDataStores, ghostdagDataStores
}

func (f *factory) dagProcesses(config *Config,
	dbManager model.DBManager,
	blockHeaderStore model.BlockHeaderStore,
	daaWindowStore model.BlocksWithTrustedDataDAAWindowStore,
	windowHeapSliceStore model.WindowHeapSliceStore,
	blockRelationStores []model.BlockRelationStore,
	reachabilityDataStores []model.ReachabilityDataStore,
	ghostdagDataStores []model.GHOSTDAGDataStore,
	isOldReachabilityInitialized bool) (
	[]model.DAGTopologyManager,
	[]model.GHOSTDAGManager,
	[]model.DAGTraversalManager,
) {

	reachabilityManagers := make([]model.ReachabilityManager, config.MaxBlockLevel+1)
	dagTopologyManagers := make([]model.DAGTopologyManager, config.MaxBlockLevel+1)
	ghostdagManagers := make([]model.GHOSTDAGManager, config.MaxBlockLevel+1)
	dagTraversalManagers := make([]model.DAGTraversalManager, config.MaxBlockLevel+1)

	newReachabilityManager := reachabilitymanager.New(
		dbManager,
		ghostdagDataStores[0],
		reachabilityDataStores[0])

	for i := 0; i <= config.MaxBlockLevel; i++ {
		if isOldReachabilityInitialized {
			reachabilityManagers[i] = reachabilitymanager.New(
				dbManager,
				ghostdagDataStores[i],
				reachabilityDataStores[i])
		} else {
			reachabilityManagers[i] = newReachabilityManager
		}

		dagTopologyManagers[i] = dagtopologymanager.New(
			dbManager,
			reachabilityManagers[i],
			blockRelationStores[i],
			ghostdagDataStores[i])

		ghostdagManagers[i] = f.ghostdagConstructor(
			dbManager,
			dagTopologyManagers[i],
			ghostdagDataStores[i],
			blockHeaderStore,
			config.K,
			config.GenesisHash)

		dagTraversalManagers[i] = dagtraversalmanager.New(
			dbManager,
			dagTopologyManagers[i],
			ghostdagDataStores[i],
			reachabilityManagers[i],
			ghostdagManagers[i],
			daaWindowStore,
			windowHeapSliceStore,
			config.GenesisHash,
			config.DifficultyAdjustmentWindowSize)
	}

	return dagTopologyManagers, ghostdagManagers, dagTraversalManagers
}
