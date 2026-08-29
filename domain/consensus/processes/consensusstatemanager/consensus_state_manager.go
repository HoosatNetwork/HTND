package consensusstatemanager

import (
	"sync"

	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/lrucache"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/multiset"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/utxo"
)

// resolveBlockStatusCacheEntry stores the cached result of ResolveBlockStatus
type resolveBlockStatusCacheEntry struct {
	status       externalapi.BlockStatus
	reversalData *model.UTXODiffReversalData
}

// consensusStateManager manages the node's consensus state
type consensusStateManager struct {
	maxBlockParents   []externalapi.KType
	mergeSetSizeLimit uint64
	genesisHash       *externalapi.DomainHash
	databaseContext   model.DBManager

	ghostdagManager       model.GHOSTDAGManager
	dagTopologyManager    model.DAGTopologyManager
	dagTraversalManager   model.DAGTraversalManager
	pastMedianTimeManager model.PastMedianTimeManager
	transactionValidator  model.TransactionValidator
	coinbaseManager       model.CoinbaseManager
	mergeDepthManager     model.MergeDepthManager
	finalityManager       model.FinalityManager
	difficultyManager     model.DifficultyManager

	headersSelectedTipStore   model.HeaderSelectedTipStore
	blockStatusStore          model.BlockStatusStore
	ghostdagDataStore         model.GHOSTDAGDataStore
	consensusStateStore       model.ConsensusStateStore
	multisetStore             model.MultisetStore
	blockStore                model.BlockStore
	utxoDiffStore             model.UTXODiffStore
	blockRelationStore        model.BlockRelationStore
	acceptanceDataStore       model.AcceptanceDataStore
	blockHeaderStore          model.BlockHeaderStore
	pruningStore              model.PruningStore
	daaBlocksStore            model.DAABlocksStore
	finalityStore             model.FinalityStore
	headersSelectedChainStore model.HeadersSelectedChainStore
	mergeDepthRootStore       model.MergeDepthRootStore
	windowHeapSliceStore      model.WindowHeapSliceStore

	stores []model.Store

	// resolveBlockStatusCache caches the results of ResolveBlockStatus calls
	resolveBlockStatusCache *lrucache.LRUCache[resolveBlockStatusCacheEntry]
	lastValidBlock          *externalapi.DomainHash

	// expensiveDiagnosticRunsRemaining caps how many times the [UTXO-DEBUG] self-consistency checks
	// in resolveSingleBlockStatus's failure branch (verifyMultisetSelfConsistency,
	// verifyAcceptanceDataAgainstDiff) will actually run a full UTXO-set scan. Those checks fire on
	// any RuleError from a new block's resolution, not just commitment mismatches - if the
	// underlying drift causes routine failures on live blocks, this prevents each one from adding a
	// multi-minute full-scan on top of the failure itself.
	expensiveDiagnosticRunsRemaining int

	// toleratedIssuesLogged tracks which inherited-offset toleration points (keyed by a short step
	// label) have already emitted their one warn line, so a full re-sync on top of an incomplete
	// imported pruning-point UTXO set logs each kind of tolerated issue once at warn and the rest at
	// debug rather than one warn per block. It is a sync.Map because some toleration points run in
	// per-transaction goroutines.
	toleratedIssuesLogged sync.Map

	// baselineOffsetPruningPoint / baselineOffset memoise pruningPointBaselineIsOffset's verdict
	// (does the current pruning point's stored multiset disagree with its own header UTXOCommitment)
	// against the pruning point hash it was computed for, so it re-evaluates only when the pruning
	// point advances.
	baselineOffsetPruningPoint *externalapi.DomainHash
	baselineOffset             bool
}

// New instantiates a new ConsensusStateManager
func New(
	databaseContext model.DBManager,
	maxBlockParents []externalapi.KType,
	mergeSetSizeLimit uint64,
	genesisHash *externalapi.DomainHash,

	ghostdagManager model.GHOSTDAGManager,
	dagTopologyManager model.DAGTopologyManager,
	dagTraversalManager model.DAGTraversalManager,
	pastMedianTimeManager model.PastMedianTimeManager,
	transactionValidator model.TransactionValidator,
	coinbaseManager model.CoinbaseManager,
	mergeDepthManager model.MergeDepthManager,
	finalityManager model.FinalityManager,
	difficultyManager model.DifficultyManager,

	blockStatusStore model.BlockStatusStore,
	ghostdagDataStore model.GHOSTDAGDataStore,
	consensusStateStore model.ConsensusStateStore,
	multisetStore model.MultisetStore,
	blockStore model.BlockStore,
	utxoDiffStore model.UTXODiffStore,
	blockRelationStore model.BlockRelationStore,
	acceptanceDataStore model.AcceptanceDataStore,
	blockHeaderStore model.BlockHeaderStore,
	headersSelectedTipStore model.HeaderSelectedTipStore,
	pruningStore model.PruningStore,
	daaBlocksStore model.DAABlocksStore,
	finalityStore model.FinalityStore,
	headersSelectedChainStore model.HeadersSelectedChainStore,
	mergeDepthRootStore model.MergeDepthRootStore,
	windowHeapSliceStore model.WindowHeapSliceStore,
	resolveBlockStatusCacheSize int,
) (model.ConsensusStateManager, error) {
	csm := &consensusStateManager{
		maxBlockParents:   maxBlockParents,
		mergeSetSizeLimit: mergeSetSizeLimit,
		genesisHash:       genesisHash,

		databaseContext: databaseContext,

		ghostdagManager:       ghostdagManager,
		dagTopologyManager:    dagTopologyManager,
		dagTraversalManager:   dagTraversalManager,
		pastMedianTimeManager: pastMedianTimeManager,
		transactionValidator:  transactionValidator,
		coinbaseManager:       coinbaseManager,
		mergeDepthManager:     mergeDepthManager,
		finalityManager:       finalityManager,
		difficultyManager:     difficultyManager,

		multisetStore:             multisetStore,
		blockStore:                blockStore,
		blockStatusStore:          blockStatusStore,
		ghostdagDataStore:         ghostdagDataStore,
		consensusStateStore:       consensusStateStore,
		utxoDiffStore:             utxoDiffStore,
		blockRelationStore:        blockRelationStore,
		acceptanceDataStore:       acceptanceDataStore,
		blockHeaderStore:          blockHeaderStore,
		headersSelectedTipStore:   headersSelectedTipStore,
		pruningStore:              pruningStore,
		daaBlocksStore:            daaBlocksStore,
		finalityStore:             finalityStore,
		headersSelectedChainStore: headersSelectedChainStore,
		mergeDepthRootStore:       mergeDepthRootStore,
		windowHeapSliceStore:      windowHeapSliceStore,
		resolveBlockStatusCache:   lrucache.New[resolveBlockStatusCacheEntry](resolveBlockStatusCacheSize, false),

		expensiveDiagnosticRunsRemaining: 3,

		stores: []model.Store{
			consensusStateStore,
			acceptanceDataStore,
			blockStore,
			blockStatusStore,
			blockRelationStore,
			multisetStore,
			ghostdagDataStore,
			consensusStateStore,
			utxoDiffStore,
			blockHeaderStore,
			headersSelectedTipStore,
			pruningStore,
			daaBlocksStore,
			finalityStore,
			headersSelectedChainStore,
			mergeDepthRootStore,
			windowHeapSliceStore,
		},
	}
	stagingArea := model.NewStagingArea()

	csm.consensusStateStore.StageVirtualUTXODiff(stagingArea, utxo.NewUTXODiff())
	csm.utxoDiffStore.Stage(stagingArea, csm.genesisHash, utxo.NewUTXODiff(), nil)
	csm.multisetStore.Stage(stagingArea, csm.genesisHash, multiset.New())

	return csm, nil
}
