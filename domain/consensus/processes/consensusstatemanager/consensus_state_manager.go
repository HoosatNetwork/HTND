package consensusstatemanager

import (
	"github.com/HoosatNetwork/HTND/domain/consensus/database"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/constants"
	"sync"
	"time"

	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/hashset"
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

	// boundaryOffsetConfirmedPruningPoint records the pruning point for which the first block above
	// it (selected parent == the pruning point) has demonstrably failed its own UTXO commitment
	// check, even though the pruning point's own stored multiset hashes correctly against its own
	// header (see confirmBaselineOffsetIfBoundaryBlock). pruningPointBaselineIsOffset's usual check
	// re-hashes that same pruning point multiset on every call and would keep reporting it verified
	// forever, because the offset only becomes visible one block later. This makes it visible from
	// here on, until the pruning point advances past the confirmed one. See HTN-208.
	boundaryOffsetConfirmedPruningPoint *externalapi.DomainHash

	// rejectionReasons carries, per block hash, why each of that block's merge-set transactions was
	// not accepted, from applyMergeSetBlocks to the survey record. Only populated when the UTXO survey
	// is enabled. See stashRejectionReasons for why it is a side channel and not a return value.
	rejectionReasons sync.Map

	// verifiedBlocksSinceCheckpoint counts blocks that passed every UTXO check, so the survey can
	// record positive evidence of health rather than leaving an empty file that means either
	// "nothing failed" or "nobody was watching".
	verifiedBlocksMutex           sync.Mutex
	verifiedBlocksSinceCheckpoint int

	// missingInput* accumulate, and periodically report, transactions this node refused during block
	// acceptance because it does not hold a coin they spend. See noteAcceptanceRejection. Guarded by
	// a mutex because acceptance runs transactions in per-block goroutines.
	missingInputMutex         sync.Mutex
	missingInputRejections    int
	missingInputLastReport    time.Time
	missingInputExample       string
	missingInputExampleIndex  uint32
	missingInputExampleTx     string
	missingInputExampleBlock  string
	missingInputExampleCount  int
	missingInputExampleInputs int

	// refuseMismatchedImportedPruningPointUTXOSet makes an imported pruning point UTXO set that does
	// not hash to its own header commitment a hard failure, so IBD moves on to another peer instead of
	// building the whole node on it. Off by default: on the current network no peer has a matching
	// set, so refusing every one of them means never syncing at all.
	refuseMismatchedImportedPruningPointUTXOSet bool

	// powScores derives the version of the block that would be built on a selected parent (see
	// versionOfChildOf), which governs virtual's parents limit and tip ordering.
	powScores []uint64

	// knownFinalityViolatingTips memoises which tips isViolatingFinality has already confirmed violate
	// finality, so findNextPendingTip - called on every single ResolveVirtual chunk while backlog
	// remains, and re-checking every current DAG tip each time - does not repeat the same expensive
	// check on the same already-dead tip over and over. Safe because the check is monotonic:
	// isViolatingFinality asks whether the current finality point (or pruning point, whichever is
	// later) is an ancestor of the tip, and both of those only move forward over the node's lifetime,
	// never back - so a tip that fails this once can never pass it later. Protected by the same outer
	// consensus lock every ResolveVirtual call already holds, like the other memoisation fields above.
	knownFinalityViolatingTips hashset.HashSet
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
	refuseMismatchedImportedPruningPointUTXOSet bool,
	powScores []uint64,
) (model.ConsensusStateManager, error) {
	csm := &consensusStateManager{
		powScores:         powScores,
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

		knownFinalityViolatingTips: hashset.New(),

		expensiveDiagnosticRunsRemaining: 3,

		refuseMismatchedImportedPruningPointUTXOSet: refuseMismatchedImportedPruningPointUTXOSet,

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

// versionOfChildOf returns the block version a block built on selectedParent has, derived from the selected parent's
// DAA score as this node computed it. Activation scores are far apart, so this is the child's own version except at an
// activation boundary, and it is the same on every node - unlike the process-global version, which depends on the
// node's uptime and IBD. The global remains only without an activation table or while the parent's DAA score is
// unknown (genesis, trusted-data bootstrap).
func (csm *consensusStateManager) versionOfChildOf(stagingArea *model.StagingArea,
	selectedParent *externalapi.DomainHash,
) (uint16, error) {
	if len(csm.powScores) == 0 || selectedParent == nil {
		return constants.GetBlockVersion(), nil
	}
	daaScore, err := csm.daaBlocksStore.DAAScore(csm.databaseContext, stagingArea, selectedParent)
	if database.IsNotFoundError(err) {
		return constants.GetBlockVersion(), nil
	}
	if err != nil {
		return 0, err
	}
	return constants.BlockVersionForDAAScore(csm.powScores, daaScore), nil
}

// maxBlockParentsForVersion returns the parents limit of blockVersion, using the last entry for a shorter table.
func (csm *consensusStateManager) maxBlockParentsForVersion(blockVersion uint16) externalapi.KType {
	index := max(int(blockVersion)-1, 0)
	if index >= len(csm.maxBlockParents) {
		index = len(csm.maxBlockParents) - 1
	}
	return csm.maxBlockParents[index]
}
