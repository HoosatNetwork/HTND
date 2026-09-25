package consensus

import (
	"fmt"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/HoosatNetwork/HTND/util/memory"
	"github.com/HoosatNetwork/HTND/util/mstime"

	"github.com/HoosatNetwork/HTND/domain/consensus/database"
	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/ruleerrors"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/hardforks"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/utxo"
	"github.com/HoosatNetwork/HTND/domain/exodus"
	"github.com/HoosatNetwork/HTND/infrastructure/logger"
	"github.com/HoosatNetwork/HTND/util/staging"
	"github.com/HoosatNetwork/HTND/version"
	"github.com/pkg/errors"
)

type consensus struct {
	lock            *sync.Mutex
	databaseContext model.DBManager

	genesisBlock *externalapi.DomainBlock
	genesisHash  *externalapi.DomainHash

	// targetTimePerBlock and difficultyAdjustmentWindowSize are kept as the raw per-block-version
	// tables rather than a single precomputed window, because the active block version is not known
	// when the consensus is constructed - see expectedDAAWindowDurationInMilliseconds.
	targetTimePerBlock             []time.Duration
	difficultyAdjustmentWindowSize []int

	// powScores is the network's activation table, kept so that a block version can be derived from
	// a DAA score without going through the process-global (see the hardforks package).
	powScores []uint64

	blockProcessor        model.BlockProcessor
	blockBuilder          model.BlockBuilder
	consensusStateManager model.ConsensusStateManager
	transactionValidator  model.TransactionValidator
	syncManager           model.SyncManager
	pastMedianTimeManager model.PastMedianTimeManager
	blockValidator        model.BlockValidator
	coinbaseManager       model.CoinbaseManager
	dagTopologyManagers   []model.DAGTopologyManager
	dagTraversalManager   model.DAGTraversalManager
	difficultyManager     model.DifficultyManager
	ghostdagManagers      []model.GHOSTDAGManager
	headerTipsManager     model.HeadersSelectedTipManager
	mergeDepthManager     model.MergeDepthManager
	pruningManager        model.PruningManager
	reachabilityManager   model.ReachabilityManager
	finalityManager       model.FinalityManager
	pruningProofManager   model.PruningProofManager

	acceptanceDataStore                 model.AcceptanceDataStore
	blockStore                          model.BlockStore
	blockHeaderStore                    model.BlockHeaderStore
	pruningStore                        model.PruningStore
	ghostdagDataStores                  []model.GHOSTDAGDataStore
	blockRelationStores                 []model.BlockRelationStore
	blockStatusStore                    model.BlockStatusStore
	consensusStateStore                 model.ConsensusStateStore
	headersSelectedTipStore             model.HeaderSelectedTipStore
	multisetStore                       model.MultisetStore
	reachabilityDataStore               model.ReachabilityDataStore
	utxoDiffStore                       model.UTXODiffStore
	finalityStore                       model.FinalityStore
	headersSelectedChainStore           model.HeadersSelectedChainStore
	daaBlocksStore                      model.DAABlocksStore
	blocksWithTrustedDataDAAWindowStore model.BlocksWithTrustedDataDAAWindowStore
	windowHeapSliceStore                model.WindowHeapSliceStore

	consensusEventsChan chan externalapi.ConsensusEvent
	virtualNotUpdated   bool
	// virtualChangeSetDropped is set when a change set describing an already committed virtual
	// change was not delivered, and reported on the next one that is. Guarded by lock.
	virtualChangeSetDropped bool
	// disqualificationStreak watches for a node that disqualifies every block it adds. Guarded by lock.
	disqualificationStreak disqualificationStreak
}

func (s *consensus) exportPruningPointExodusBundle(pruningPoint *externalapi.DomainHash, exportRoot, network string) {
	onEnd := logger.LogAndMeasureExecutionTime(log, "[AUTO-EXODUS] exportPruningPointExodusBundle")
	defer onEnd()

	header, err := s.GetBlockHeader(pruningPoint)
	if err != nil {
		log.Errorf("[AUTO-EXODUS] FAILED: could not fetch header for pruning point %s: %s", pruningPoint, err)
		return
	}
	daaScore := header.DAAScore()
	bundleDir := filepath.Join(exportRoot, "pruning-point-"+fmt.Sprintf("%d-%s", daaScore, pruningPoint))
	log.Infof("[AUTO-EXODUS] exporting pruning point %s (DAA score %d) from acceptance data to %s",
		pruningPoint, daaScore, bundleDir)

	writer, err := exodus.NewWriter(bundleDir, exodus.BundleTarget{
		BlockHash: pruningPoint,
		DAAScore:  daaScore,
	}, exodus.DefaultChunkEntryCount)
	if err != nil {
		log.Errorf("[AUTO-EXODUS] FAILED: could not create bundle at %s: %s", bundleDir, err)
		return
	}

	err = s.IterateUTXOSetAtBlockFromAcceptanceData(pruningPoint,
		func(outpoint *externalapi.DomainOutpoint, entry externalapi.UTXOEntry) error {
			return writer.AddEntry(outpoint, entry)
		})
	if err != nil {
		log.Errorf("[AUTO-EXODUS] FAILED: acceptance-data UTXO walk for pruning point %s failed: %s (partial bundle: %s)",
			pruningPoint, err, bundleDir)
		return
	}

	commitment, err := writer.Finalize(exodus.BundleMeta{
		ToolVersion: version.Version(),
		NodeVersion: version.Version(),
		Network:     network,
	})
	if err != nil {
		log.Errorf("[AUTO-EXODUS] FAILED: could not finalize bundle for pruning point %s at %s: %s",
			pruningPoint, bundleDir, err)
		return
	}

	headerCommitment := header.UTXOCommitment()
	if commitment.Equal(headerCommitment) {
		log.Infof("[AUTO-EXODUS] PASS: pruning point %s (DAA score %d) exported %d UTXOs; computed commitment %s matches header commitment %s; bundle=%s",
			pruningPoint, daaScore, writer.EntryCount(), commitment, headerCommitment, bundleDir)
		return
	}
	log.Errorf("[AUTO-EXODUS] FAIL: pruning point %s (DAA score %d) exported %d UTXOs; computed commitment %s does not match header commitment %s; bundle=%s",
		pruningPoint, daaScore, writer.EntryCount(), commitment, headerCommitment, bundleDir)
}

// In order to prevent a situation that the consensus lock is held for too much time, we
// release the lock each time we resolve 100 blocks.
// Note: `virtualResolveChunk` should be smaller than `params.FinalityDuration` in order to avoid a situation
// where UpdatePruningPointByVirtual skips a pruning point.
const virtualResolveChunk = 100

// resolveVirtualChunkSlowLogThreshold is the minimum duration before we log chunk timing at INFO.
// This helps diagnose cases where resolving virtual appears to stall after IBD.
const resolveVirtualChunkSlowLogThreshold = 5 * time.Second

// resolveVirtualChunkHeartbeat is an INFO log emitted while a single resolve-virtual chunk is still running.
// This helps distinguish a very slow chunk from a hard deadlock.
const resolveVirtualChunkHeartbeat = 30 * time.Second

func (s *consensus) ValidateAndInsertBlockWithTrustedData(block *externalapi.BlockWithTrustedData, validateUTXO bool) error {
	s.lock.Lock()
	defer s.lock.Unlock()

	_, _, err := s.blockProcessor.ValidateAndInsertBlockWithTrustedData(block, validateUTXO)
	if err != nil {
		return err
	}
	return nil
}

// Init initializes consensus
func (s *consensus) Init(skipAddingGenesis bool) error {
	s.lock.Lock()
	defer s.lock.Unlock()

	onEnd := logger.LogAndMeasureExecutionTime(log, "Init")
	defer onEnd()

	stagingArea := model.NewStagingArea()

	exists, err := s.blockStatusStore.Exists(s.databaseContext, stagingArea, model.VirtualGenesisBlockHash)
	if err != nil {
		return err
	}

	// There should always be a virtual genesis block. Initially only the genesis points to this block, but
	// on a node with pruned header all blocks without known parents points to it.
	if !exists {
		s.blockStatusStore.Stage(stagingArea, model.VirtualGenesisBlockHash, externalapi.StatusUTXOValid)
		err = s.reachabilityManager.Init(stagingArea)
		if err != nil {
			return err
		}

		for _, dagTopologyManager := range s.dagTopologyManagers {
			err = dagTopologyManager.SetParents(stagingArea, model.VirtualGenesisBlockHash, nil)
			if err != nil {
				return err
			}
		}

		s.consensusStateStore.StageTips(stagingArea, []*externalapi.DomainHash{model.VirtualGenesisBlockHash})
		for _, ghostdagDataStore := range s.ghostdagDataStores {
			ghostdagDataStore.Stage(stagingArea, model.VirtualGenesisBlockHash, externalapi.NewBlockGHOSTDAGData(
				0,
				big.NewInt(0),
				nil,
				nil,
				nil,
				nil,
				externalapi.KType(1)), false)
		}

		s.daaBlocksStore.StageDAAScore(stagingArea, model.VirtualGenesisBlockHash, 0)
		s.daaBlocksStore.StageBlockDAAAddedBlocks(stagingArea, model.VirtualGenesisBlockHash, nil)

		err = staging.CommitAllChanges(s.databaseContext, stagingArea)
		if err != nil {
			return err
		}
	}

	// The genesis should be added to the DAG if it's a fresh consensus, unless said otherwise (on a
	// case where the consensus is used for a pruned headers node).
	if !skipAddingGenesis && s.blockStore.Count(stagingArea) == 0 {
		genesisWithTrustedData := &externalapi.BlockWithTrustedData{
			Block:     s.genesisBlock,
			DAAWindow: nil,
			GHOSTDAGData: []*externalapi.BlockGHOSTDAGDataHashPair{
				{
					GHOSTDAGData: externalapi.NewBlockGHOSTDAGData(0, big.NewInt(0), model.VirtualGenesisBlockHash, nil, nil, make(map[externalapi.DomainHash]externalapi.KType), externalapi.KType(1)),
					Hash:         s.genesisHash,
				},
			},
		}
		_, _, err = s.blockProcessor.ValidateAndInsertBlockWithTrustedData(genesisWithTrustedData, true)
		if err != nil {
			return err
		}
	}

	// Deliberately not fatal. This is a recovery path for an already-damaged database, and it also
	// runs for the staging consensus built mid-IBD; a node that is merely stuck must never be turned
	// into a node that refuses to start.
	err = s.repairCollapsedVirtualIfRequired()
	if err != nil {
		log.Errorf("Failed to repair a virtual colored with dynamicK=0: %+v. The node will start, but "+
			"until virtual is recolored it may build block templates whose merge depth root disagrees "+
			"with the one validators apply, and have its own blocks rejected.", err)
	}

	// Start goroutine to display cache sizes every minute
	if os.Getenv("HTND_PROFILER") != "" {
		go s.displayCacheSizes()
		go s.displayMemUse()
		go func() {
			if err := s.periodicLogFrees(); err != nil {
				log.Warnf("periodicLogFrees exited with error: %v", err)
			}
		}()
	}

	// go s.periodicFreeOSMemory()

	return nil
}

// repairCollapsedVirtualIfRequired re-colors virtual at startup when its stored GHOSTDAG data was
// written with a K of zero.
//
// A stored dynamicK of 0 is not a K anything computed - it is the zero value that GHOSTDAG used to
// leave behind on its cached-K path, and virtual, being re-colored on every update while always
// having stored data, was the one block that reliably hit it. Reading that 0 back as K makes
// checkBlueCandidate's `len(mergeSetBlues) == k+1` fire on the first candidate, so only the selected
// parent stays blue and virtual's blue score advances by exactly 1 per update however wide the DAG
// is.
//
// The damage that surfaces is not the coloring but the merge depth root derived from it. A block
// built on virtual's parents is colored independently by every validator (a fresh hash always misses
// the cache, so it always gets a real K), so it has virtual's blue score plus the blues virtual
// failed to count. Its requiredBlueScore is correspondingly higher, and its merge depth root can
// therefore be one chain block NEWER than the root boundedMergeBreakingParents used when it approved
// those same parents. Any branch that forked in the gap between the two roots passes the filter and
// fails the validator: the node keeps mining blocks that it rejects itself, with no way out, because
// virtual is only re-colored when a block arrives and no block can be accepted.
//
// So the repair cannot wait for the next block. Guarded on the poison marker so a healthy node does
// no startup work.
func (s *consensus) repairCollapsedVirtualIfRequired() error {
	stagingArea := model.NewStagingArea()

	virtualGHOSTDAGData, err := s.ghostdagDataStores[0].Get(s.databaseContext, stagingArea, model.VirtualBlockHash, false)
	if err != nil {
		if database.IsNotFoundError(err) {
			return nil
		}
		return err
	}
	if virtualGHOSTDAGData.DynamicK() != 0 {
		return nil
	}

	virtualParents, err := s.dagTopologyManagers[0].Parents(stagingArea, model.VirtualBlockHash)
	if err != nil {
		if database.IsNotFoundError(err) {
			return nil
		}
		return err
	}
	// With a single parent the collapse has no observable effect: there is nothing to merge, so the
	// blue score is right either way and the parent set cannot be over-permissive.
	if len(virtualParents) < 2 {
		return nil
	}

	log.Warnf("Virtual's stored GHOSTDAG data has dynamicK=0 across %d parents, which means it was "+
		"colored with a K that lets nothing but the selected parent be blue. Recomputing virtual so "+
		"its blue score, and the merge depth root derived from it, agree with what a block built on "+
		"these parents will be validated against.", len(virtualParents))

	err = s.consensusStateManager.RecomputeVirtual()
	if err != nil {
		return errors.Wrap(err, "failed to recompute a virtual that was colored with dynamicK=0")
	}

	log.Infof("Virtual recomputed successfully")
	return nil
}

func (s *consensus) periodicLogFrees() error {
	minutes := 1
	time.Sleep(time.Duration(minutes) * time.Minute)

	ticker := time.NewTicker(time.Duration(minutes) * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		memory.LogLeaks()
	}
	return nil
}

func (s *consensus) displayCacheSizes() {
	ticker := time.NewTicker(30 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		log.Infof("BlockStore cache size: %d", s.blockStore.CacheLen())
		log.Infof("BlockHeaderStore cache size: %d", s.blockHeaderStore.CacheLen())
		log.Infof("BlockStatusStore cache size: %d", s.blockStatusStore.CacheLen())
		log.Infof("AcceptanceDataStore cache size: %d", s.acceptanceDataStore.CacheLen())
		log.Infof("MultisetStore cache size: %d", s.multisetStore.CacheLen())
		log.Infof("UTXODiffStore cache size: %d", s.utxoDiffStore.CacheLen())
		log.Infof("ConsensusStateStore cache size: %d", s.consensusStateStore.CacheLen())
		log.Infof("DAABlocksStore cache size: %d", s.daaBlocksStore.CacheLen())
		log.Infof("DAAWindowStore cache size: %d", s.blocksWithTrustedDataDAAWindowStore.CacheLen())
		log.Infof("FinalityStore cache size: %d", s.finalityStore.CacheLen())
		log.Infof("HeadersSelectedChainStore cache size: %d", s.headersSelectedChainStore.CacheLen())

		var cacheLen int
		for i := 1; i < len(s.blockRelationStores); i++ {
			cacheLen += s.blockRelationStores[i].CacheLen()
		}
		log.Infof("BlockRelationStore[x] cache size sum: %d", cacheLen)
		log.Infof("ReachabilityDataStore cache size: %d", s.reachabilityDataStore.CacheLen())
		cacheLen = 0
		for i := 1; i < len(s.blockRelationStores); i++ {
			cacheLen += s.ghostdagDataStores[i].CacheLen()
		}
		log.Infof("GHOSTDAGDataStore[x] cache size sum: %d", cacheLen)
		log.Infof("ResolveBlockStatus cache size: %d", s.consensusStateManager.ResolveBlockStatusCacheLen())
		log.Infof("PruningStore cache size: %d", s.pruningStore.CacheLen())
		log.Infof("WindowHeapSliceStore cache size: %d", s.windowHeapSliceStore.CacheLen())
	}
}

func (s *consensus) displayMemUse() {
	ticker := time.NewTicker(30 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		log.Infof("Num Coroutines %d", runtime.NumGoroutine())
		log.Infof("HeapAlloc: %d MB", m.HeapAlloc/1024/1024)
		log.Infof("HeapSys:   %d MB", m.HeapSys/1024/1024)
		log.Infof("Sys:       %d MB", m.Sys/1024/1024)
		log.Infof("HeapReleased: %d MB", m.HeapReleased/1024/1024)
	}
}

func (s *consensus) PruningPointAndItsAnticone() ([]*externalapi.DomainHash, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	return s.pruningManager.PruningPointAndItsAnticone()
}

// BuildBlock builds a block over the current state, with the transactions
// selected by the given transactionSelector
func (s *consensus) BuildBlock(coinbaseData *externalapi.DomainCoinbaseData,
	transactions []*externalapi.DomainTransaction,
) (*externalapi.DomainBlock, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	if err := s.ensureVirtualUpdatedNoLock(); err != nil {
		return nil, err
	}

	block, _, err := s.blockBuilder.BuildBlock(coinbaseData, transactions)
	return block, err
}

// BuildBlockTemplate builds a block over the current state, with the transactions
// selected by the given transactionSelector plus metadata information related to
// coinbase rewards and node sync status
func (s *consensus) BuildBlockTemplate(coinbaseData *externalapi.DomainCoinbaseData,
	transactions []*externalapi.DomainTransaction,
) (*externalapi.DomainBlockTemplate, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	if err := s.ensureVirtualUpdatedNoLock(); err != nil {
		return nil, err
	}

	block, hasRedReward, err := s.blockBuilder.BuildBlock(coinbaseData, transactions)
	if err != nil {
		return nil, err
	}

	isNearlySynced, err := s.isNearlySyncedNoLock()
	if err != nil {
		return nil, err
	}

	return &externalapi.DomainBlockTemplate{
		Block:                block,
		CoinbaseData:         coinbaseData,
		CoinbaseHasRedReward: hasRedReward,
		IsNearlySynced:       isNearlySynced,
	}, nil
}

// ValidateAndInsertBlock validates the given block and, if valid, applies it
// to the current state
func (s *consensus) ValidateAndInsertBlock(block *externalapi.DomainBlock, updateVirtual bool, powSkip bool) error {
	if updateVirtual {
		s.lock.Lock()
		if s.virtualNotUpdated {
			// We enter the loop in locked state
			for {
				_, isCompletelyResolved, err := s.resolveVirtualChunkNoLock(virtualResolveChunk)
				if errors.Is(err, externalapi.ErrVirtualHasNoUsableTip) {
					// Nothing is left to resolve: every tip is disqualified or invalid. Refusing the
					// block for that froze the node - a new block is the only thing that can give
					// virtual a usable tip again, and every insertion failed on this same error before
					// it got the chance. Insert it; it gets its own status below.
					log.Warnf("Inserting block %s although virtual has no usable tip to resolve: %s",
						consensushashing.BlockHash(block), err)
					s.virtualNotUpdated = false
					err = nil
					isCompletelyResolved = true
				}
				if err != nil {
					s.lock.Unlock()
					return err
				}
				if isCompletelyResolved {
					// Make sure we enter the block insertion function w/o releasing the lock.
					// Otherwise, we might actually enter it in `s.virtualNotUpdated == true` state
					_, err = s.validateAndInsertBlockNoLock(block, updateVirtual, powSkip)
					// Finally, unlock for the last iteration and return
					s.lock.Unlock()
					if err != nil {
						return err
					}
					return nil
				}
				// Unlock to allow other threads to enter consensus
				s.lock.Unlock()
				// Lock for the next iteration
				s.lock.Lock()
			}
		}
		_, err := s.validateAndInsertBlockNoLock(block, updateVirtual, powSkip)
		s.lock.Unlock()
		if err != nil {
			return err
		}
		return nil
	}

	return s.validateAndInsertBlockWithLock(block, updateVirtual, powSkip)
}

func (s *consensus) validateAndInsertBlockWithLock(block *externalapi.DomainBlock, updateVirtual bool, powSkip bool) error {
	s.lock.Lock()
	defer s.lock.Unlock()

	_, err := s.validateAndInsertBlockNoLock(block, updateVirtual, powSkip)
	if err != nil {
		return err
	}
	return nil
}

func (s *consensus) validateAndInsertBlockNoLock(block *externalapi.DomainBlock, updateVirtual bool, powSkip bool) (*externalapi.VirtualChangeSet, error) {
	virtualChangeSet, blockStatus, err := s.blockProcessor.ValidateAndInsertBlock(block, updateVirtual, powSkip)
	if err != nil {
		return nil, err
	}

	// If block has a body, and yet virtual was not updated -- signify that virtual is in non-updated state
	if !updateVirtual && blockStatus != externalapi.StatusHeaderOnly {
		s.virtualNotUpdated = true
	}
	if updateVirtual && blockStatus != externalapi.StatusHeaderOnly {
		s.noteInsertedBlockStatus(consensushashing.BlockHash(block))
	}

	err = s.sendBlockAddedEvent(block, blockStatus)
	if err != nil {
		// Virtual is already committed, and returning here means its change set is never sent either
		s.noteVirtualChangeSetDropped(virtualChangeSet, updateVirtual)
		return nil, err
	}

	err = s.sendVirtualChangedEvent(virtualChangeSet, updateVirtual)
	if err != nil {
		return nil, err
	}

	return virtualChangeSet, nil
}

func (s *consensus) sendBlockAddedEvent(block *externalapi.DomainBlock, blockStatus externalapi.BlockStatus) error {
	if s.consensusEventsChan != nil {
		if blockStatus == externalapi.StatusHeaderOnly || blockStatus == externalapi.StatusInvalid {
			return nil
		}

		if len(s.consensusEventsChan) == cap(s.consensusEventsChan) {
			return errors.Errorf("consensusEventsChan is full")
		}
		s.consensusEventsChan <- &externalapi.BlockAdded{Block: block}
	}
	return nil
}

// noteVirtualChangeSetDropped records that a change set was not delivered. Virtual is committed before
// its change set is sent, so an undelivered one is a diff lost to every consumer that replays diffs -
// the UTXO index - and the next delivered change set must say so.
func (s *consensus) noteVirtualChangeSetDropped(virtualChangeSet *externalapi.VirtualChangeSet, wasVirtualUpdated bool) {
	if wasVirtualUpdated && s.consensusEventsChan != nil && virtualChangeSet != nil {
		s.virtualChangeSetDropped = true
	}
}

func (s *consensus) sendVirtualChangedEvent(virtualChangeSet *externalapi.VirtualChangeSet, wasVirtualUpdated bool) (err error) {
	if !wasVirtualUpdated || s.consensusEventsChan == nil || virtualChangeSet == nil {
		return nil
	}
	defer func() {
		if err != nil {
			s.noteVirtualChangeSetDropped(virtualChangeSet, wasVirtualUpdated)
		}
	}()

	if len(s.consensusEventsChan) == cap(s.consensusEventsChan) {
		return errors.Errorf("consensusEventsChan is full")
	}

	stagingArea := model.NewStagingArea()
	virtualGHOSTDAGData, err := s.ghostdagDataStores[0].Get(s.databaseContext, stagingArea, model.VirtualBlockHash, false)
	if database.IsNotFoundError(err) {
		log.Infof("sendVirtualChangedEvent failed to retrieve with %s\n", model.VirtualBlockHash)
		return err
	}
	if err != nil {
		return err
	}

	virtualSelectedParentGHOSTDAGData, err := s.ghostdagDataStores[0].Get(s.databaseContext, stagingArea, virtualGHOSTDAGData.SelectedParent(), false)
	if err != nil {
		return err
	}

	virtualDAAScore, err := s.daaBlocksStore.DAAScore(s.databaseContext, stagingArea, model.VirtualBlockHash)
	if err != nil {
		return err
	}

	// Populate the change set with additional data before sending
	virtualChangeSet.VirtualSelectedParentBlueScore = virtualSelectedParentGHOSTDAGData.BlueScore()
	virtualChangeSet.VirtualDAAScore = virtualDAAScore
	virtualChangeSet.EarlierChangeSetsDropped = s.virtualChangeSetDropped

	s.consensusEventsChan <- virtualChangeSet
	s.virtualChangeSetDropped = false
	return nil
}

// ValidateTransactionAndPopulateWithConsensusData validates the given transaction
// and populates it with any missing consensus data
func (s *consensus) ValidateTransactionAndPopulateWithConsensusData(transaction *externalapi.DomainTransaction) error {
	s.lock.Lock()
	defer s.lock.Unlock()
	stagingArea := model.NewStagingArea()

	daaScore, err := s.daaBlocksStore.DAAScore(s.databaseContext, stagingArea, model.VirtualBlockHash)
	if err != nil {
		return err
	}

	err = s.transactionValidator.ValidateTransactionInIsolation(transaction, daaScore)
	if err != nil {
		return err
	}

	err = s.consensusStateManager.PopulateTransactionWithUTXOEntries(stagingArea, transaction)
	if err != nil {
		return err
	}

	virtualPastMedianTime, err := s.pastMedianTimeManager.PastMedianTime(stagingArea, model.VirtualBlockHash)
	if err != nil {
		return err
	}

	err = s.transactionValidator.ValidateTransactionInContextIgnoringUTXO(stagingArea, transaction, model.VirtualBlockHash, virtualPastMedianTime, daaScore)
	if err != nil {
		return err
	}
	return s.transactionValidator.ValidateTransactionInContextAndPopulateFee(
		stagingArea, transaction, model.VirtualBlockHash, daaScore)
}

func (s *consensus) GetBlock(blockHash *externalapi.DomainHash) (*externalapi.DomainBlock, bool, error) {
	s.lock.Lock()
	defer s.lock.Unlock()
	stagingArea := model.NewStagingArea()

	block, err := s.blockStore.Block(s.databaseContext, stagingArea, blockHash)
	if err != nil {
		if errors.Is(err, database.ErrNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return block, true, nil
}

func (s *consensus) HasBlock(blockHash *externalapi.DomainHash) (bool, error) {
	s.lock.Lock()
	defer s.lock.Unlock()
	stagingArea := model.NewStagingArea()

	exists, err := s.blockStore.HasBlock(s.databaseContext, stagingArea, blockHash)
	if err != nil {
		if errors.Is(err, database.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	return exists, nil
}

func (s *consensus) GetBlockEvenIfHeaderOnly(blockHash *externalapi.DomainHash) (*externalapi.DomainBlock, error) {
	s.lock.Lock()
	defer s.lock.Unlock()
	stagingArea := model.NewStagingArea()

	block, err := s.blockStore.Block(s.databaseContext, stagingArea, blockHash)
	if err == nil {
		return block, nil
	}
	if !errors.Is(err, database.ErrNotFound) {
		return nil, err
	}

	header, err := s.blockHeaderStore.BlockHeader(s.databaseContext, stagingArea, blockHash)
	if err != nil {
		if errors.Is(err, database.ErrNotFound) {
			return nil, errors.Wrapf(err, "block %s does not exist", blockHash)
		}
		return nil, err
	}

	return &externalapi.DomainBlock{Header: header}, nil
}

func (s *consensus) GetBlockHeader(blockHash *externalapi.DomainHash) (externalapi.BlockHeader, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	stagingArea := model.NewStagingArea()

	blockHeader, err := s.blockHeaderStore.BlockHeader(s.databaseContext, stagingArea, blockHash)
	if err != nil {
		if errors.Is(err, database.ErrNotFound) {
			return nil, errors.Wrapf(err, "block header %s does not exist", blockHash)
		}
		return nil, err
	}
	return blockHeader, nil
}

// GetBlockHeaders returns headers for the given hashes using a single staging area to minimize overhead.
func (s *consensus) GetBlockHeaders(blockHashes []*externalapi.DomainHash) ([]externalapi.BlockHeader, error) {
	s.lock.Lock()
	defer s.lock.Unlock()
	stagingArea := model.NewStagingArea()

	headers, err := s.blockHeaderStore.BlockHeaders(s.databaseContext, stagingArea, blockHashes)
	if err != nil {
		return nil, err
	}
	return headers, nil
}

func (s *consensus) GetBlockInfo(blockHash *externalapi.DomainHash) (*externalapi.BlockInfo, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	stagingArea := model.NewStagingArea()

	blockInfo := &externalapi.BlockInfo{}

	exists, err := s.blockStatusStore.Exists(s.databaseContext, stagingArea, blockHash)
	if err != nil {
		return nil, err
	}
	blockInfo.Exists = exists
	if !exists {
		return blockInfo, nil
	}

	blockStatus, err := s.blockStatusStore.Get(s.databaseContext, stagingArea, blockHash)
	if database.IsNotFoundError(err) {
		log.Infof("GetBlockInfo failed to retrieve with %s\n", blockHash)
		return nil, err
	}
	if err != nil {
		return nil, err
	}
	blockInfo.BlockStatus = blockStatus

	// If the status is invalid, then we don't have the necessary reachability data to check if it's in PruningPoint.Future.
	if blockStatus == externalapi.StatusInvalid {
		return blockInfo, nil
	}

	ghostdagData, err := s.ghostdagDataStores[0].Get(s.databaseContext, stagingArea, blockHash, false)
	if database.IsNotFoundError(err) {
		log.Infof("GetBlockInfo failed to retrieve with %s\n", blockHash)
		return nil, err
	}
	if err != nil {
		return nil, err
	}

	blockInfo.BlueScore = ghostdagData.BlueScore()
	blockInfo.BlueWork = ghostdagData.BlueWork()
	blockInfo.DynamicK = ghostdagData.DynamicK()
	blockInfo.SelectedParent = ghostdagData.SelectedParent()
	blockInfo.MergeSetBlues = ghostdagData.MergeSetBlues()
	blockInfo.MergeSetReds = ghostdagData.MergeSetReds()

	return blockInfo, nil
}

func (s *consensus) GetBlockRelations(blockHash *externalapi.DomainHash) (
	parents []*externalapi.DomainHash, children []*externalapi.DomainHash, err error,
) {
	s.lock.Lock()
	defer s.lock.Unlock()

	stagingArea := model.NewStagingArea()

	blockRelation, err := s.blockRelationStores[0].BlockRelation(s.databaseContext, stagingArea, blockHash)
	if err != nil {
		return nil, nil, err
	}

	return blockRelation.Parents, blockRelation.Children, nil
}

func (s *consensus) GetBlockAcceptanceData(blockHash *externalapi.DomainHash) (externalapi.AcceptanceData, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	stagingArea := model.NewStagingArea()

	err := s.validateBlockHashExists(stagingArea, blockHash)
	if err != nil {
		return nil, err
	}

	return s.acceptanceDataStore.Get(s.databaseContext, stagingArea, blockHash)
}

func (s *consensus) GetBlocksAcceptanceData(blockHashes []*externalapi.DomainHash) ([]externalapi.AcceptanceData, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	blocksAcceptanceData := make([]externalapi.AcceptanceData, len(blockHashes))

	for i := range blockHashes {
		// Use a separate staging area for each acceptance data retrieval to avoid memory accumulation
		stagingArea := model.NewStagingArea()

		acceptanceData, err := s.acceptanceDataStore.Get(s.databaseContext, stagingArea, blockHashes[i])

		if database.IsNotFoundError(err) {
			log.Infof("GetBlocksAcceptanceData failed to retrieve with %s\n", blockHashes[i])
			return nil, err
		}
		if err != nil {
			return nil, err
		}

		blocksAcceptanceData[i] = acceptanceData
	}

	return blocksAcceptanceData, nil
}

func (s *consensus) GetHashesBetween(lowHash, highHash *externalapi.DomainHash, maxBlocks uint64, brute bool) (
	hashes []*externalapi.DomainHash, actualHighHash *externalapi.DomainHash, err error,
) {
	s.lock.Lock()
	defer s.lock.Unlock()

	stagingArea := model.NewStagingArea()

	err = s.validateBlockHashExists(stagingArea, lowHash)
	if err != nil {
		return nil, nil, err
	}
	err = s.validateBlockHashExists(stagingArea, highHash)
	if err != nil {
		return nil, nil, err
	}

	return s.syncManager.GetHashesBetween(stagingArea, lowHash, highHash, maxBlocks, brute)
}

func (s *consensus) GetAnticone(blockHash, contextHash *externalapi.DomainHash,
	maxBlocks uint64,
) (hashes []*externalapi.DomainHash, err error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	stagingArea := model.NewStagingArea()

	err = s.validateBlockHashExists(stagingArea, blockHash)
	if err != nil {
		return nil, err
	}
	err = s.validateBlockHashExists(stagingArea, contextHash)
	if err != nil {
		return nil, err
	}

	return s.syncManager.GetAnticone(stagingArea, blockHash, contextHash, maxBlocks)
}

func (s *consensus) GetMissingBlockBodyHashes(highHash *externalapi.DomainHash) ([]*externalapi.DomainHash, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	stagingArea := model.NewStagingArea()

	err := s.validateBlockHashExists(stagingArea, highHash)
	if err != nil {
		return nil, err
	}

	return s.syncManager.GetMissingBlockBodyHashes(stagingArea, highHash)
}

func (s *consensus) GetPruningPointUTXOs(expectedPruningPointHash *externalapi.DomainHash,
	fromOutpoint *externalapi.DomainOutpoint, limit int,
) ([]*externalapi.OutpointAndUTXOEntryPair, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	stagingArea := model.NewStagingArea()

	pruningPointHash, err := s.pruningStore.PruningPoint(s.databaseContext, stagingArea)
	if err != nil {
		return nil, err
	}

	if !expectedPruningPointHash.Equal(pruningPointHash) {
		return nil, errors.Wrapf(ruleerrors.ErrWrongPruningPointHash, "expected pruning point %s but got %s",
			expectedPruningPointHash,
			pruningPointHash)
	}

	// HTN-005's serve half, gated at hardforks.RefuseMismatchedImportVersion: once the pruning
	// point's commitment is treated as law, a node whose own set does not hash to it must not hand
	// that set to anyone else.
	//
	// This is the loop that makes the condition spread. MuHash is homomorphic, so an offset imported
	// once propagates unchanged into every block resolved forward, and every peer that syncs from
	// this node inherits the same gap - which is why the tolerant population grows rather than
	// shrinks. GetInfo already advertises the same fact through UTXOSetHealth, so a peer can see it
	// before asking; this is what stops the answer being given anyway.
	//
	// The gate is unscheduled, so this is inert today, and it must stay that way until a coordinated
	// rebaseline: essentially every node currently serves a set that fails this check, so refusing
	// now would simply stop IBD working for everyone.
	if err := s.refuseToServeUnverifiableUTXOSet(stagingArea, pruningPointHash); err != nil {
		return nil, err
	}

	pruningPointUTXOs, err := s.pruningStore.PruningPointUTXOs(s.databaseContext, fromOutpoint, limit)
	if err != nil {
		return nil, err
	}
	return pruningPointUTXOs, nil
}

// refuseToServeUnverifiableUTXOSet returns a rule error when hardforks.RefuseMismatchedImportVersion
// has activated for pruningPointHash and this node's own UTXO baseline does not hash to that point's
// header commitment.
//
// Caller must hold s.lock: it reads through consensusStateManager.UTXOSetHealth, the same unlocked
// form the exported UTXOSetHealth wraps.
//
// Only a definite negative refuses. Health that was never Checked - a node still on genesis, or one
// whose pruning point or multiset is not readable - is not evidence of a bad set, and refusing on it
// would take nodes offline for a bookkeeping gap rather than for serving bad data.
func (s *consensus) refuseToServeUnverifiableUTXOSet(stagingArea *model.StagingArea,
	pruningPointHash *externalapi.DomainHash,
) error {
	if !hardforks.IsScheduled(hardforks.RefuseMismatchedImportVersion) {
		return nil
	}
	if len(s.powScores) == 0 {
		return nil
	}

	header, err := s.blockHeaderStore.BlockHeader(s.databaseContext, stagingArea, pruningPointHash)
	if err != nil {
		return err
	}
	blockVersion := constants.BlockVersionForDAAScore(s.powScores, header.DAAScore())
	if !hardforks.Active(hardforks.RefuseMismatchedImportVersion, blockVersion) {
		return nil
	}

	health := s.consensusStateManager.UTXOSetHealth(stagingArea)
	if health == nil || !health.Checked || health.BaselineVerified {
		return nil
	}

	return errors.Wrapf(ruleerrors.ErrBadPruningPointUTXOSet,
		"refusing to serve the pruning point %s UTXO set: this node's stored multiset (%s) does not "+
			"match the commitment in that point's own header (%s), and serving it would pass this "+
			"node's own offset on to the peer",
		pruningPointHash, health.StoredMultiset, health.HeaderCommitment)
}

// virtualUTXOEntriesChunkSize bounds how long GetVirtualUTXOEntries holds the consensus lock at a
// stretch. A lookup costs about 10µs against mainnet's UTXO set, so a chunk holds the lock for
// roughly 10ms, and block processing gets the lock back between chunks however many coins an
// address has.
const virtualUTXOEntriesChunkSize = 1024

// GetVirtualUTXOEntries looks each outpoint up in virtual's UTXO set - the set this node itself
// spends from - and returns its entry, or nil where the set does not hold the coin.
//
// It is called from RPC handlers, so it must neither stall behind block processing nor stall it. It
// used to take the consensus lock with Lock() and hold it for the whole batch. Block processing holds
// that lock through a pruning point UTXO set update - measured at 15 seconds to nearly 4 minutes on
// mainnet nodes, every few hours - so a GetUtxosByAddresses arriving then queued for the whole update
// and reached its client as DeadlineExceeded, where before the check existed the same call answered
// from the UTXO index at once. And an address with 100,000 coins held the lock for 1.4 seconds,
// pausing block processing for that long.
//
// So the lock is taken per chunk, and only if it can be had within maxWait; when it cannot, the call
// returns ok=false and the caller serves what it would have without the check. Each chunk is checked
// against virtual as it stands when that chunk runs, so a block accepted between chunks can show in
// later chunks and not earlier ones - but every answer is one virtual actually gave.
//
// It also returns virtual's parents as they stood during the lookup, or nil if they changed between
// chunks. A caller comparing the answers with a secondary index can tell from them whether the index
// described the same virtual state: an index that has not yet applied virtual's latest change still
// lists coins virtual has just removed, which is the index catching up, not drift.
func (s *consensus) GetVirtualUTXOEntries(outpoints []*externalapi.DomainOutpoint, maxWait time.Duration) (
	[]externalapi.UTXOEntry, []*externalapi.DomainHash, bool, error,
) {
	entries := make([]externalapi.UTXOEntry, len(outpoints))
	var virtualParents []*externalapi.DomainHash
	for start := 0; start < len(outpoints); start += virtualUTXOEntriesChunkSize {
		end := min(start+virtualUTXOEntriesChunkSize, len(outpoints))
		if !tryLockFor(s.lock, maxWait) {
			return nil, nil, false, nil
		}
		chunkVirtualParents, err := s.virtualUTXOEntriesNoLock(outpoints[start:end], entries[start:end])
		s.lock.Unlock()
		if err != nil {
			return nil, nil, false, err
		}
		switch {
		case start == 0:
			virtualParents = chunkVirtualParents
		case !externalapi.HashesEqual(virtualParents, chunkVirtualParents):
			virtualParents = nil
		}
	}
	return entries, virtualParents, true, nil
}

// virtualUTXOEntriesNoLock fills entries with virtual's entry for each outpoint and returns virtual's
// parents. One lookup answers both "is it there" and "what is it": asking HasUTXOByOutpoint first
// doubled the database reads - Has never consults the UTXO cache - for a question the lookup's
// not-found already answers.
func (s *consensus) virtualUTXOEntriesNoLock(outpoints []*externalapi.DomainOutpoint, entries []externalapi.UTXOEntry) (
	[]*externalapi.DomainHash, error,
) {
	stagingArea := model.NewStagingArea()
	virtualParents, err := s.dagTopologyManagers[0].Parents(stagingArea, model.VirtualBlockHash)
	if err != nil {
		return nil, err
	}
	for i, outpoint := range outpoints {
		// Does not populate the cache on a miss: an address with more coins than the cache holds
		// would otherwise scan through it evicting everything block validation put there, going cold
		// for the path that actually needs it, for no benefit to itself - see HTN-207.
		entry, found, err := s.consensusStateStore.UTXOByOutpointWithoutPopulatingCache(s.databaseContext, stagingArea, outpoint)
		if database.IsNotFoundError(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if found {
			entries[i] = entry
		}
	}
	return externalapi.CloneHashes(virtualParents), nil
}

// tryLockFor takes mu if it becomes free within maxWait, polling rather than queueing: Lock() waits
// for as long as the holder keeps the lock, and this gives up so the caller can answer without it.
func tryLockFor(mu *sync.Mutex, maxWait time.Duration) bool {
	if mu.TryLock() {
		return true
	}
	deadline := time.Now().Add(maxWait)
	sleep := 50 * time.Microsecond
	for time.Now().Before(deadline) {
		time.Sleep(min(sleep, time.Until(deadline)))
		if mu.TryLock() {
			return true
		}
		sleep = min(sleep*2, 5*time.Millisecond)
	}
	return false
}

func (s *consensus) GetVirtualUTXOs(expectedVirtualParents []*externalapi.DomainHash,
	fromOutpoint *externalapi.DomainOutpoint, limit int,
) ([]*externalapi.OutpointAndUTXOEntryPair, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	stagingArea := model.NewStagingArea()

	virtualParents, err := s.dagTopologyManagers[0].Parents(stagingArea, model.VirtualBlockHash)
	if err != nil {
		return nil, err
	}

	if !externalapi.HashesEqual(expectedVirtualParents, virtualParents) {
		return nil, errors.Wrapf(ruleerrors.ErrGetVirtualUTXOsWrongVirtualParents, "expected virtual parents %s but got %s",
			expectedVirtualParents,
			virtualParents)
	}

	virtualUTXOs, err := s.consensusStateStore.VirtualUTXOs(s.databaseContext, fromOutpoint, limit)
	if err != nil {
		return nil, err
	}
	return virtualUTXOs, nil
}

// IterateUTXOSetAtBlock streams the full UTXO set as of the given (past, UTXO-valid) block,
// invoking callback once for every outpoint/entry pair, while holding the consensus lock for
// the duration of the iteration. See externalapi.Consensus for details.
func (s *consensus) IterateUTXOSetAtBlock(blockHash *externalapi.DomainHash,
	callback func(outpoint *externalapi.DomainOutpoint, entry externalapi.UTXOEntry) error,
) error {
	s.lock.Lock()
	defer s.lock.Unlock()

	stagingArea := model.NewStagingArea()

	utxoSetIterator, err := s.consensusStateManager.RestorePastUTXOSetIterator(stagingArea, blockHash)
	if err != nil {
		return err
	}
	defer utxoSetIterator.Close()

	for ok := utxoSetIterator.First(); ok; ok = utxoSetIterator.Next() {
		outpoint, entry, err := utxoSetIterator.Get()
		if err != nil {
			return err
		}
		err = callback(outpoint, entry)
		if err != nil {
			return err
		}
	}

	return nil
}

func (s *consensus) PruningPoint() (*externalapi.DomainHash, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	stagingArea := model.NewStagingArea()

	return s.pruningStore.PruningPoint(s.databaseContext, stagingArea)
}

func (s *consensus) PruningPointHeaders() ([]externalapi.BlockHeader, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	lastPruningPointIndex, err := s.pruningStore.CurrentPruningPointIndex(s.databaseContext, model.NewStagingArea())
	if err != nil {
		return nil, err
	}

	headers := make([]externalapi.BlockHeader, 0, lastPruningPointIndex)
	for i := uint64(0); i <= lastPruningPointIndex; i++ {
		// Use separate staging areas for each retrieval to avoid memory accumulation
		pruningStagingArea := model.NewStagingArea()
		pruningPoint, err := s.pruningStore.PruningPointByIndex(s.databaseContext, pruningStagingArea, i)
		if err != nil {
			return nil, err
		}

		headerStagingArea := model.NewStagingArea()
		header, err := s.blockHeaderStore.BlockHeader(s.databaseContext, headerStagingArea, pruningPoint)
		if err != nil {
			return nil, err
		}

		headers = append(headers, header)
	}

	return headers, nil
}

func (s *consensus) ClearImportedPruningPointData() error {
	s.lock.Lock()
	defer s.lock.Unlock()

	return s.pruningManager.ClearImportedPruningPointData()
}

func (s *consensus) AppendImportedPruningPointUTXOs(outpointAndUTXOEntryPairs []*externalapi.OutpointAndUTXOEntryPair) error {
	s.lock.Lock()
	defer s.lock.Unlock()

	return s.pruningManager.AppendImportedPruningPointUTXOs(outpointAndUTXOEntryPairs)
}

func (s *consensus) ValidateAndInsertImportedPruningPoint(newPruningPoint *externalapi.DomainHash) error {
	s.lock.Lock()
	defer s.lock.Unlock()

	return s.blockProcessor.ValidateAndInsertImportedPruningPoint(newPruningPoint)
}

func (s *consensus) GetVirtualSelectedParent() (*externalapi.DomainHash, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	stagingArea := model.NewStagingArea()

	virtualGHOSTDAGData, err := s.ghostdagDataStores[0].Get(s.databaseContext, stagingArea, model.VirtualBlockHash, false)
	if database.IsNotFoundError(err) {
		log.Debugf("GetVirtualSelectedParent failed to retrieve with %s\n", model.VirtualBlockHash)
		return nil, err
	}
	if err != nil {
		return nil, err
	}
	return virtualGHOSTDAGData.SelectedParent(), nil
}

func (s *consensus) Tips() ([]*externalapi.DomainHash, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	stagingArea := model.NewStagingArea()

	return s.consensusStateStore.Tips(stagingArea, s.databaseContext)
}

func (s *consensus) GetVirtualInfo() (*externalapi.VirtualInfo, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	stagingArea := model.NewStagingArea()

	blockRelations, err := s.blockRelationStores[0].BlockRelation(s.databaseContext, stagingArea, model.VirtualBlockHash)
	if err != nil {
		return nil, err
	}
	bits, err := s.difficultyManager.RequiredDifficulty(stagingArea, model.VirtualBlockHash)
	if err != nil {
		return nil, err
	}
	pastMedianTime, err := s.pastMedianTimeManager.PastMedianTime(stagingArea, model.VirtualBlockHash)
	if err != nil {
		return nil, err
	}
	virtualGHOSTDAGData, err := s.ghostdagDataStores[0].Get(s.databaseContext, stagingArea, model.VirtualBlockHash, false)
	if database.IsNotFoundError(err) {
		log.Infof("GetVirtualInfo failed to retrieve with %s\n", model.VirtualBlockHash)
		return nil, err
	}
	if err != nil {
		return nil, err
	}

	daaScore, err := s.daaBlocksStore.DAAScore(s.databaseContext, stagingArea, model.VirtualBlockHash)
	if err != nil {
		return nil, err
	}

	return &externalapi.VirtualInfo{
		ParentHashes:   blockRelations.Parents,
		Bits:           bits,
		PastMedianTime: pastMedianTime,
		BlueScore:      virtualGHOSTDAGData.BlueScore(),
		DAAScore:       daaScore,
	}, nil
}

func (s *consensus) GetVirtualDAAScore() (uint64, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	stagingArea := model.NewStagingArea()

	return s.daaBlocksStore.DAAScore(s.databaseContext, stagingArea, model.VirtualBlockHash)
}

func (s *consensus) CreateBlockLocatorFromPruningPoint(highHash *externalapi.DomainHash, limit uint32) (externalapi.BlockLocator, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	stagingArea := model.NewStagingArea()

	err := s.validateBlockHashExists(stagingArea, highHash)
	if err != nil {
		return nil, err
	}

	pruningPoint, err := s.pruningStore.PruningPoint(s.databaseContext, stagingArea)
	if err != nil {
		return nil, err
	}

	return s.syncManager.CreateBlockLocator(stagingArea, pruningPoint, highHash, limit)
}

func (s *consensus) CreateFullHeadersSelectedChainBlockLocator() (externalapi.BlockLocator, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	stagingArea := model.NewStagingArea()

	lowHash, err := s.pruningStore.PruningPoint(s.databaseContext, stagingArea)
	if err != nil {
		return nil, err
	}
	log.Debugf("Found pruning point %s as lowHash", lowHash)

	highHash, err := s.headersSelectedTipStore.HeadersSelectedTip(s.databaseContext, stagingArea)
	if err != nil {
		return nil, err
	}
	log.Debugf("Found headers selected tip %s as highHash", highHash)

	return s.syncManager.CreateHeadersSelectedChainBlockLocator(stagingArea, lowHash, highHash)
}

func (s *consensus) CreateHeadersSelectedChainBlockLocator(lowHash, highHash *externalapi.DomainHash) (externalapi.BlockLocator, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	stagingArea := model.NewStagingArea()

	return s.syncManager.CreateHeadersSelectedChainBlockLocator(stagingArea, lowHash, highHash)
}

func (s *consensus) GetSyncInfo() (*externalapi.SyncInfo, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	stagingArea := model.NewStagingArea()

	return s.syncManager.GetSyncInfo(stagingArea)
}

func (s *consensus) IsValidPruningPoint(blockHash *externalapi.DomainHash) (bool, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	stagingArea := model.NewStagingArea()

	err := s.validateBlockHashExists(stagingArea, blockHash)
	if err != nil {
		return false, err
	}

	return s.pruningManager.IsValidPruningPoint(stagingArea, blockHash)
}

func (s *consensus) ValidateLowHashIsFunctionalPruningPoint(lowHash *externalapi.DomainHash) (*externalapi.DomainHash, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	stagingArea := model.NewStagingArea()

	if _, err := s.headersSelectedChainStore.GetIndexByHash(s.databaseContext, stagingArea, lowHash); err != nil {
		// This is extremely rare case when pruning point is not in the headers selected chain store,
		// so lets find a pruning point that is in the selected chain store by brute force.
		pruningPointIndex, err := s.pruningStore.CurrentPruningPointIndex(s.databaseContext, stagingArea)
		if err != nil {
			return nil, err
		}
		var i uint64
		for i = 1; i < pruningPointIndex; i++ {
			lowHash, err = s.pruningStore.PruningPointByIndex(s.databaseContext, stagingArea, pruningPointIndex-i)
			if err != nil {
				return nil, err
			}
			var lowHashIndex uint64
			lowHashIndex, err = s.headersSelectedChainStore.GetIndexByHash(s.databaseContext, stagingArea, lowHash)
			if err != nil {
				return nil, err
			}
			if lowHashIndex > 0 {
				break
			}
		}
	}
	return lowHash, nil
}

func (s *consensus) ArePruningPointsViolatingFinality(pruningPoints []externalapi.BlockHeader) (bool, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	stagingArea := model.NewStagingArea()

	return s.pruningManager.ArePruningPointsViolatingFinality(stagingArea, pruningPoints)
}

func (s *consensus) ImportPruningPoints(pruningPoints []externalapi.BlockHeader) error {
	s.lock.Lock()
	defer s.lock.Unlock()

	stagingArea := model.NewStagingArea()
	err := s.consensusStateManager.ImportPruningPoints(stagingArea, pruningPoints)
	if err != nil {
		return err
	}

	err = staging.CommitAllChanges(s.databaseContext, stagingArea)
	if err != nil {
		return err
	}

	return nil
}

func (s *consensus) GetVirtualSelectedParentChainFromBlock(blockHash *externalapi.DomainHash) (*externalapi.SelectedChainPath, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	stagingArea := model.NewStagingArea()

	err := s.validateBlockHashExists(stagingArea, blockHash)
	if err != nil {
		return nil, err
	}

	return s.consensusStateManager.GetVirtualSelectedParentChainFromBlock(stagingArea, blockHash)
}

func (s *consensus) validateBlockHashExists(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash) error {
	status, err := s.blockStatusStore.Get(s.databaseContext, stagingArea, blockHash)
	if database.IsNotFoundError(err) {
		return errors.Wrapf(err, "block %s does not exist", blockHash)
	}
	if err != nil {
		return err
	}

	if status == externalapi.StatusInvalid {
		return errors.Errorf("block %s is invalid", blockHash)
	}
	return nil
}

func (s *consensus) IsInSelectedParentChainOf(blockHashA *externalapi.DomainHash, blockHashB *externalapi.DomainHash) (bool, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	stagingArea := model.NewStagingArea()

	err := s.validateBlockHashExists(stagingArea, blockHashA)
	if err != nil {
		return false, err
	}
	err = s.validateBlockHashExists(stagingArea, blockHashB)
	if err != nil {
		return false, err
	}

	return s.dagTopologyManagers[0].IsInSelectedParentChainOf(stagingArea, blockHashA, blockHashB)
}

func (s *consensus) GetHeadersSelectedTip() (*externalapi.DomainHash, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	stagingArea := model.NewStagingArea()

	return s.headersSelectedTipStore.HeadersSelectedTip(s.databaseContext, stagingArea)
}

func (s *consensus) Anticone(blockHash *externalapi.DomainHash) ([]*externalapi.DomainHash, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	stagingArea := model.NewStagingArea()

	err := s.validateBlockHashExists(stagingArea, blockHash)
	if err != nil {
		return nil, err
	}

	tips, err := s.consensusStateStore.Tips(stagingArea, s.databaseContext)
	if err != nil {
		return nil, err
	}

	return s.dagTraversalManager.AnticoneFromBlocks(stagingArea, tips, blockHash, 0)
}

func (s *consensus) EstimateNetworkHashesPerSecond(startHash *externalapi.DomainHash, windowSize int) (uint64, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	return s.difficultyManager.EstimateNetworkHashesPerSecond(startHash, windowSize)
}

func (s *consensus) PopulateMass(transaction *externalapi.DomainTransaction) {
	s.transactionValidator.PopulateMass(transaction)
}

func (s *consensus) ResolveVirtual(progressReportCallback func(uint64, uint64)) error {
	virtualDAAScoreStart, err := s.GetVirtualDAAScore()
	if err != nil {
		return err
	}
	log.Infof("Start of virtual DAAScore %d", virtualDAAScoreStart)

	for i := 0; ; i++ {
		if i%10 == 0 && progressReportCallback != nil {
			virtualDAAScore, err := s.GetVirtualDAAScore()
			if err != nil {
				return err
			}
			progressReportCallback(virtualDAAScoreStart, virtualDAAScore)
		}

		_, isCompletelyResolved, err := s.resolveVirtualChunkWithLock(virtualResolveChunk)
		if err != nil {
			return err
		}
		if isCompletelyResolved {
			break
		}
	}

	// After the resolve loop, before return nil
	stagingArea := model.NewStagingArea()
	tips, err := s.consensusStateStore.Tips(stagingArea, s.databaseContext)
	if err != nil {
		return err
	}
	if len(tips) == 0 {
		return errors.Errorf("ResolveVirtual finished with zero tips")
	}

	hasUsableTip := false
	for _, tip := range tips {
		status, err := s.blockStatusStore.Get(s.databaseContext, stagingArea, tip)
		if err != nil {
			continue
		}
		if status == externalapi.StatusUTXOValid || status == externalapi.StatusUTXOPendingVerification {
			hasUsableTip = true
			break
		}
	}
	if !hasUsableTip {
		return errors.WithStack(externalapi.ErrVirtualHasNoUsableTip)
	}

	parents, err := s.dagTopologyManagers[0].Parents(stagingArea, model.VirtualBlockHash)
	if err != nil {
		return err
	}
	if len(parents) == 1 && parents[0].Equal(model.VirtualGenesisBlockHash) {
		return errors.Errorf("ResolveVirtual finished but virtual parents are still VirtualGenesis")
	}

	daa, err := s.GetVirtualDAAScore()
	if err != nil {
		return err
	}
	if daa == 0 {
		return errors.Errorf("ResolveVirtual finished with virtual DAA score 0")
	}

	return nil
}

func (s *consensus) resolveVirtualChunkWithLock(maxBlocksToResolve uint64) (virtualChangeSet *externalapi.VirtualChangeSet, isCompletelyResolved bool, err error) {
	lockWaitStart := time.Now()
	s.lock.Lock()
	lockWait := time.Since(lockWaitStart)
	chunkStart := time.Now()

	chunkDone := make(chan struct{})
	heartbeat := time.AfterFunc(resolveVirtualChunkHeartbeat, func() {
		select {
		case <-chunkDone:
			return
		default:
			log.Infof("ResolveVirtual chunk still running (elapsed=%s, maxBlocks=%d, lockWait=%s)", time.Since(chunkStart), maxBlocksToResolve, lockWait)
		}
	})
	defer func() {
		close(chunkDone)
		heartbeat.Stop()

		chunkDuration := time.Since(chunkStart)
		if lockWait >= resolveVirtualChunkSlowLogThreshold || chunkDuration >= resolveVirtualChunkSlowLogThreshold {
			log.Infof("ResolveVirtual chunk finished (maxBlocks=%d, lockWait=%s, duration=%s, complete=%t, err=%v)", maxBlocksToResolve, lockWait, chunkDuration, isCompletelyResolved, err)
		}

		s.lock.Unlock()
	}()

	virtualChangeSet, isCompletelyResolved, err = s.resolveVirtualChunkNoLock(maxBlocksToResolve)
	return virtualChangeSet, isCompletelyResolved, err
}

// ensureVirtualUpdatedNoLock drains any pending virtual resolution, exactly like
// ValidateAndInsertBlock does before it touches the DAG. Must be called with s.lock held.
// Without this, a block template built while virtual is only partially resolved (e.g. mid
// IBD or a large reorg, resolved in virtualResolveChunk-sized steps) reads blue score/DAA
// score/parents off an intermediate virtual snapshot that's about to be superseded, rather
// than the state real validation will eventually judge the mined block against.
func (s *consensus) ensureVirtualUpdatedNoLock() error {
	for s.virtualNotUpdated {
		_, isCompletelyResolved, err := s.resolveVirtualChunkNoLock(virtualResolveChunk)
		if err != nil {
			return err
		}
		if isCompletelyResolved {
			return nil
		}
		// Unlock to allow other threads to enter consensus, then relock for the next chunk.
		s.lock.Unlock()
		s.lock.Lock()
	}
	return nil
}

func (s *consensus) resolveVirtualChunkNoLock(maxBlocksToResolve uint64) (*externalapi.VirtualChangeSet, bool, error) {
	virtualChangeSet, isCompletelyResolved, err := s.consensusStateManager.ResolveVirtual(maxBlocksToResolve)
	if err != nil {
		return nil, false, err
	}
	s.virtualNotUpdated = !isCompletelyResolved

	stagingArea := model.NewStagingArea()
	err = s.pruningManager.UpdatePruningPointByVirtual(stagingArea)
	if err != nil {
		return nil, false, err
	}

	err = staging.CommitAllChanges(s.databaseContext, stagingArea)
	if err != nil {
		return nil, false, err
	}

	err = s.pruningManager.UpdatePruningPointIfRequired()
	if err != nil {
		return nil, false, err
	}

	err = s.sendVirtualChangedEvent(virtualChangeSet, true)
	if err != nil {
		return nil, false, err
	}

	return virtualChangeSet, isCompletelyResolved, nil
}

func (s *consensus) BuildPruningPointProof() (*externalapi.PruningPointProof, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	return s.pruningProofManager.BuildPruningPointProof(model.NewStagingArea())
}

func (s *consensus) ValidatePruningPointProof(pruningPointProof *externalapi.PruningPointProof) error {
	s.lock.Lock()
	defer s.lock.Unlock()

	log.Infof("Validating the pruning point proof")
	err := s.pruningProofManager.ValidatePruningPointProof(pruningPointProof)
	if err != nil {
		return err
	}

	log.Infof("Done validating the pruning point proof")
	return nil
}

func (s *consensus) ApplyPruningPointProof(pruningPointProof *externalapi.PruningPointProof) error {
	s.lock.Lock()
	defer s.lock.Unlock()

	log.Infof("Applying the pruning point proof")
	err := s.pruningProofManager.ApplyPruningPointProof(pruningPointProof)
	if err != nil {
		return err
	}

	log.Infof("Done applying the pruning point proof")
	return nil
}

func (s *consensus) BlockDAAWindowHashes(blockHash *externalapi.DomainHash) ([]*externalapi.DomainHash, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	stagingArea := model.NewStagingArea()
	return s.dagTraversalManager.DAABlockWindow(stagingArea, blockHash)
}

func (s *consensus) TrustedDataDataDAAHeader(trustedBlockHash, daaBlockHash *externalapi.DomainHash, daaBlockWindowIndex uint64) (*externalapi.TrustedDataDataDAAHeader, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	stagingArea := model.NewStagingArea()
	header, err := s.blockHeaderStore.BlockHeader(s.databaseContext, stagingArea, daaBlockHash)
	if err != nil {
		return nil, err
	}

	ghostdagData, err := s.ghostdagDataStores[0].Get(s.databaseContext, stagingArea, daaBlockHash, false)
	if err != nil && !database.IsNotFoundError(err) {
		return nil, err
	}

	if err == nil {
		return &externalapi.TrustedDataDataDAAHeader{
			Header:       header,
			GHOSTDAGData: ghostdagData,
		}, nil
	}

	// GHOSTDAG data not found in store: the block is below this node's pruning point and exists here
	// only as an entry in trustedBlockHash's trusted DAA window.
	ghostdagData, err = s.trustedWindowGHOSTDAGData(stagingArea, trustedBlockHash, daaBlockHash, daaBlockWindowIndex)
	if err != nil {
		log.Infof("TrustedDataDataDAAHeader failed to retrieve with %s\n", daaBlockHash)
		return nil, err
	}

	return &externalapi.TrustedDataDataDAAHeader{
		Header:       header,
		GHOSTDAGData: ghostdagData,
	}, nil
}

// trustedWindowGHOSTDAGData finds daaBlockHash's GHOSTDAG data in trustedBlockHash's stored trusted
// DAA window.
//
// HTN-204. This used to read the entry at daaBlockWindowIndex and return it without checking which
// block it was. daaBlockWindowIndex is the position of daaBlockHash in the list being SERVED; the
// store is indexed by position in the window as it was originally RECEIVED. Those only coincide
// while the served window happens to be exactly the received one, in the same order - which was only
// true because a headers-proof node's served window was truncated at its pruning point and never
// contained trusted-window blocks at all.
//
// Once a headers-proof node serves its full window, as it must for the next node to compute the
// right difficulty, the two orders diverge. Reading by index then either returns a DIFFERENT block's
// GHOSTDAG data under this block's header - silently wrong trusted data handed to a syncing peer - or
// runs past the end of the stored window, which is the "DAA window <hash> does not exist in db"
// failure that sank the original HTN-204 patch.
//
// The index is still tried first, because on the common path it is correct and costs one read. It
// is only trusted if the entry it names is actually daaBlockHash; otherwise the window is searched by
// hash. Nothing is returned for a hash the window does not contain.
func (s *consensus) trustedWindowGHOSTDAGData(stagingArea *model.StagingArea,
	trustedBlockHash, daaBlockHash *externalapi.DomainHash, daaBlockWindowIndex uint64,
) (*externalapi.BlockGHOSTDAGData, error) {
	pair, err := s.blocksWithTrustedDataDAAWindowStore.DAAWindowBlock(
		s.databaseContext, stagingArea, trustedBlockHash, daaBlockWindowIndex)
	if err != nil && !database.IsNotFoundError(err) {
		return nil, err
	}
	if err == nil && pair.Hash.Equal(daaBlockHash) {
		return pair.GHOSTDAGData, nil
	}

	// The trusted part of trustedBlockHash's window is not necessarily trustedBlockHash's OWN trusted
	// window. calculateBlockWindowHeap walks down the selected chain and takes the trusted window of
	// the first block it reaches that has one - so once a headers-proof node's pruning point has
	// advanced past the one it synced to, the new pruning point has no trusted window of its own, and
	// the bottom of its window is the OLD pruning point's trusted window, stored under the old hash.
	//
	// So this finds that same anchor by the same rule, rather than assuming it is trustedBlockHash.
	// Searching trustedBlockHash alone is what made a node serve correctly right after its IBD and
	// then fail to serve anyone as soon as its pruning point moved on.
	anchor, found, err := s.trustedWindowAnchor(stagingArea, trustedBlockHash)
	if err != nil {
		return nil, err
	}
	if found {
		for i := uint64(0); ; i++ {
			pair, err := s.blocksWithTrustedDataDAAWindowStore.DAAWindowBlock(
				s.databaseContext, stagingArea, anchor, i)
			if database.IsNotFoundError(err) {
				break
			}
			if err != nil {
				return nil, err
			}
			if pair.Hash.Equal(daaBlockHash) {
				return pair.GHOSTDAGData, nil
			}
		}
	}

	return nil, errors.Wrapf(database.ErrNotFound,
		"block %s is not in the trusted DAA window reachable from %s", daaBlockHash, trustedBlockHash)
}

// trustedWindowAnchor returns the block whose stored trusted DAA window forms the bottom of
// blockHash's DAA window: blockHash itself if it has one, otherwise the first block down its selected
// chain that does. This must follow exactly the rule calculateBlockWindowHeap uses to decide where to
// take the trusted window from, or the server will look for a block in a different window from the
// one it served it out of.
func (s *consensus) trustedWindowAnchor(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash,
) (*externalapi.DomainHash, bool, error) {
	current := blockHash
	for {
		_, err := s.blocksWithTrustedDataDAAWindowStore.DAAWindowBlock(s.databaseContext, stagingArea, current, 0)
		if err == nil {
			return current, true, nil
		}
		if !database.IsNotFoundError(err) {
			return nil, false, err
		}

		ghostdagData, err := s.ghostdagDataStores[0].Get(s.databaseContext, stagingArea, current, false)
		if database.IsNotFoundError(err) {
			return nil, false, nil
		}
		if err != nil {
			return nil, false, err
		}
		selectedParent := ghostdagData.SelectedParent()
		if selectedParent == nil || selectedParent.Equal(s.genesisHash) ||
			selectedParent.Equal(model.VirtualGenesisBlockHash) {
			return nil, false, nil
		}
		current = selectedParent
	}
}

func (s *consensus) TrustedBlockAssociatedGHOSTDAGDataBlockHashes(blockHash *externalapi.DomainHash) ([]*externalapi.DomainHash, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	return s.pruningManager.TrustedBlockAssociatedGHOSTDAGDataBlockHashes(model.NewStagingArea(), blockHash)
}

func (s *consensus) TrustedGHOSTDAGData(blockHash *externalapi.DomainHash) (*externalapi.BlockGHOSTDAGData, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	stagingArea := model.NewStagingArea()
	ghostdagData, err := s.ghostdagDataStores[0].Get(s.databaseContext, stagingArea, blockHash, false)
	isNotFoundError := database.IsNotFoundError(err)
	if isNotFoundError || ghostdagData.SelectedParent().Equal(model.VirtualGenesisBlockHash) {
		return s.ghostdagDataStores[0].Get(s.databaseContext, stagingArea, blockHash, true)
	}

	return ghostdagData, nil
}

func (s *consensus) IsChainBlock(blockHash *externalapi.DomainHash) (bool, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	stagingArea := model.NewStagingArea()
	virtualGHOSTDAGData, err := s.ghostdagDataStores[0].Get(s.databaseContext, stagingArea, model.VirtualBlockHash, false)
	if database.IsNotFoundError(err) {
		log.Infof("IsChainBlock failed to retrieve with %s\n", model.VirtualBlockHash)
		return false, err
	}
	if err != nil {
		return false, err
	}

	return s.dagTopologyManagers[0].IsInSelectedParentChainOf(stagingArea, blockHash, virtualGHOSTDAGData.SelectedParent())
}

func (s *consensus) VirtualMergeDepthRoot() (*externalapi.DomainHash, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	stagingArea := model.NewStagingArea()
	return s.mergeDepthManager.VirtualMergeDepthRoot(stagingArea)
}

// IsNearlySynced returns whether this consensus is considered synced or close to being synced. This info
// is used to determine if it's ok to use a block template from this node for mining purposes.
func (s *consensus) IsNearlySynced() (bool, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	return s.isNearlySyncedNoLock()
}

// UTXOSetHealth reports whether this node's UTXO baseline hashes to the commitment it is supposed
// to hash to. See externalapi.UTXOSetHealth: "is this node synced" and "is this node's data
// correct" are different questions, and this answers the second one.
func (s *consensus) UTXOSetHealth() (*externalapi.UTXOSetHealth, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	stagingArea := model.NewStagingArea()
	return s.consensusStateManager.UTXOSetHealth(stagingArea), nil
}

// expectedDAAWindowDurationInMilliseconds returns how far behind the wall clock this node's selected
// tip may fall before it stops considering itself nearly synced.
//
// This is computed per call, from the block version that is active NOW. It used to be computed once
// in the factory, from constants.GetBlockVersion() at construction time - but blockVersion is a
// process-global atomic that starts at 1 and is only raised later, at runtime, as blocks arrive
// (blockrelay's ibd.go and handle_relay_invs.go). Nothing ever recomputed it.
//
// The result was that the window depended on when the consensus object happened to be built. A
// consensus constructed at startup (domain.New) saw version 1 and got the v1 table's
// 1s x 2641 = ~44 minutes; a staging consensus constructed mid-run during a pruning-point IBD
// (domain.InitStagingConsensus) saw version 6 and got 200ms x 2640 = ~8.8 minutes. Two nodes on the
// same binary and the same chain therefore used different thresholds, and since transaction relay is
// switched off entirely while a node is not nearly synced, the node with the shorter window silently
// stopped relaying transactions whenever its virtual lagged by more than that - while its peers
// carried on. Same input, different answer per node, which is the worst property this predicate can
// have.
func (s *consensus) expectedDAAWindowDurationInMilliseconds() int64 {
	index := int(constants.GetBlockVersion()) - 1
	if index < 0 {
		index = 0
	}
	// Clamp rather than panic: SetBlockVersion is a one-way ratchet driven by relayed blocks, so a
	// version beyond the tables is reachable from the network, and an index panic here would take
	// down a node over a peer-supplied value.
	if index >= len(s.targetTimePerBlock) {
		index = len(s.targetTimePerBlock) - 1
	}
	windowIndex := index
	if windowIndex >= len(s.difficultyAdjustmentWindowSize) {
		windowIndex = len(s.difficultyAdjustmentWindowSize) - 1
	}
	if index < 0 || windowIndex < 0 {
		return 0
	}
	return s.targetTimePerBlock[index].Milliseconds() * int64(s.difficultyAdjustmentWindowSize[windowIndex])
}

func (s *consensus) isNearlySyncedNoLock() (bool, error) {
	stagingArea := model.NewStagingArea()
	virtualGHOSTDAGData, err := s.ghostdagDataStores[0].Get(s.databaseContext, stagingArea, model.VirtualBlockHash, false)
	if err != nil {
		return false, err
	}

	if virtualGHOSTDAGData.SelectedParent().Equal(s.genesisHash) || virtualGHOSTDAGData.SelectedParent().Equal(model.VirtualGenesisBlockHash) {
		return false, nil
	}

	virtualSelectedParentHeader, err := s.blockHeaderStore.BlockHeader(s.databaseContext, stagingArea, virtualGHOSTDAGData.SelectedParent())
	if err != nil {
		return false, err
	}

	now := mstime.Now().UnixMilliseconds()
	expectedDAAWindowDurationInMilliseconds := s.expectedDAAWindowDurationInMilliseconds()
	// As a heuristic, we allow the node to mine if he is likely to be within the current DAA window of fully synced nodes.
	// Such blocks contribute to security by maintaining the current difficulty despite possibly being slightly out of sync.
	if now-virtualSelectedParentHeader.TimeInMilliseconds() < expectedDAAWindowDurationInMilliseconds {
		log.Debugf("The selected tip timestamp is recent (%d), current (%d), as limit (%d) so IsNearlySynced returns true",
			virtualSelectedParentHeader.TimeInMilliseconds(), now, expectedDAAWindowDurationInMilliseconds)
		return true, nil
	}

	log.Debugf("The selected tip timestamp is old (%d), current (%d), as limit (%d) so IsNearlySynced returns false",
		virtualSelectedParentHeader.TimeInMilliseconds(), now, expectedDAAWindowDurationInMilliseconds)
	return false, nil
}

func (s *consensus) ResolveBlockStatus(blockHash *externalapi.DomainHash, useSeparateStagingAreaPerBlock bool) (externalapi.BlockStatus, error) {
	stagingArea := model.NewStagingArea()
	info, _, err := s.consensusStateManager.ResolveBlockStatus(stagingArea, blockHash, useSeparateStagingAreaPerBlock)
	return info, err
}

// mapLegacyBlockStatus maps old block status values (from a previous schema with 8 statuses)
// to the current schema (5 statuses).
// Old schema: StatusInvalid(0), StatusViolatingFinality(1), StatusErrorInTipsInDecreasingOrder(2),
//
//	StatusBlockStatusNotFound(3), StatusUTXOValid(4), StatusUTXOPendingVerification(5),
//	StatusDisqualifiedFromChain(6), StatusHeaderOnly(7)
//
// New schema: StatusInvalid(0), StatusUTXOValid(1), StatusUTXOPendingVerification(2),
//
//	StatusDisqualifiedFromChain(3), StatusHeaderOnly(4)
func mapLegacyBlockStatus(oldStatus externalapi.BlockStatus) externalapi.BlockStatus {
	switch oldStatus {
	case 0:
		// StatusInvalid -> StatusInvalid
		return externalapi.StatusInvalid
	case 1, 2, 3, 4, 5, 6, 7:
		// Legacy error states -> StatusInvalid
		return externalapi.StatusUTXOValid
	// case 4:
	// 	// StatusUTXOValid -> StatusUTXOValid (was 4, now 1)
	// 	return externalapi.StatusUTXOValid
	// case 5:
	// 	// StatusUTXOPendingVerification -> StatusUTXOPendingVerification (was 5, now 2)
	// 	return externalapi.StatusUTXOPendingVerification
	// case 6:
	// 	// StatusDisqualifiedFromChain -> StatusDisqualifiedFromChain (was 6, now 3)
	// 	return externalapi.StatusDisqualifiedFromChain
	// case 7:
	// 	// StatusHeaderOnly -> StatusHeaderOnly (was 7, now 4)
	// 	return externalapi.StatusHeaderOnly
	default:
		// Any other value (shouldn't happen) -> StatusInvalid
		return externalapi.StatusInvalid
	}
}

// RepairDisqualifiedTipChains resets the blocks that keep virtual pinned at the virtual genesis
// marker, and only those: it walks the selected parent chain down from every tip, through blocks
// that are pending verification or header-only, marks each disqualified block
// StatusUTXOPendingVerification, and stops at the first UTXO-valid (or invalid) block. It never marks
// anything UTXO-valid: every block it touches keeps the UTXO diff, multiset and acceptance data it
// was stored with, and gets fresh ones when the next resolve re-verifies it.
//
// It differs from RepairBlockStatuses in the two ways that matter for running unattended:
//
//   - It touches only the disqualified chains rather than every block in the store, so the consensus
//     lock is held for the length of the disqualified segment and not for a walk of the whole DAG.
//   - It marks the blocks pending verification rather than UTXO-valid. getUnverifiedChainBlocks
//     collects exactly the pending blocks and stops at any other status, so a block marked pending is
//     re-resolved and gets a real UTXO diff, while one marked valid is taken at its word and never
//     gets one - which is how a repaired node ends up with UTXO-valid blocks that have no diff.
//
// It stops at virtual's current selected parent unless that block is itself the tip being walked
// (see the comment in the body). Blocks that deserve their disqualification simply get it back on
// the next resolve, with a diff this time. It returns how many blocks it reset.
func (s *consensus) RepairDisqualifiedTipChains() (uint64, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	stagingArea := model.NewStagingArea()
	tips, err := s.consensusStateStore.Tips(stagingArea, s.databaseContext)
	if err != nil {
		return 0, err
	}

	// Virtual's selected parent is never reset from below. Its UTXO diff is the one stored relative
	// to virtual, and everything else's restore path ends there. Re-resolved as a non-tip block of a
	// longer chain it gets a temporary diff pointing at its selected parent, whose own diff still
	// points back at it, and the restorePastUTXO the resolve tip then runs on it walks that cycle
	// until the process runs out of memory. That was reachable before the walk-through below (a
	// disqualified tip above a disqualified virtual selected parent) and the walk-through would have
	// made it the common case. Reset as a tip it is safe: then it is the resolve tip itself.
	var virtualSelectedParent *externalapi.DomainHash
	virtualGHOSTDAGData, err := s.ghostdagDataStores[0].Get(s.databaseContext, stagingArea, model.VirtualBlockHash, false)
	if err != nil && !database.IsNotFoundError(err) {
		return 0, err
	}
	if err == nil {
		virtualSelectedParent = virtualGHOSTDAGData.SelectedParent()
	}

	reset := make(map[externalapi.DomainHash]struct{})
	walked := make(map[externalapi.DomainHash]struct{})
	for _, tip := range tips {
		current := tip
		for {
			if _, alreadyReset := reset[*current]; alreadyReset {
				break
			}
			if current.Equal(s.genesisHash) {
				break
			}
			if !current.Equal(tip) && current.Equal(virtualSelectedParent) {
				break
			}
			status, err := s.blockStatusStore.Get(s.databaseContext, stagingArea, current)
			if database.IsNotFoundError(err) {
				break
			}
			if err != nil {
				return 0, err
			}
			switch status {
			case externalapi.StatusDisqualifiedFromChain:
				s.blockStatusStore.Stage(stagingArea, current, externalapi.StatusUTXOPendingVerification)
				reset[*current] = struct{}{}
			case externalapi.StatusUTXOPendingVerification, externalapi.StatusHeaderOnly:
				// Not reset, walked through. A pending tip above a disqualified segment is the normal
				// shape after IBD or relay: stopping at it, as this used to, left the segment below it
				// untouched, so the tip was cascade-disqualified again on the very next resolve.
				// getUnverifiedChainBlocks walks through the same two statuses.
				if _, alreadyWalked := walked[*current]; alreadyWalked {
					current = nil
				} else {
					walked[*current] = struct{}{}
				}
			default:
				// UTXO-valid (or invalid): below here the chain is not what keeps virtual pinned.
				current = nil
			}
			if current == nil {
				break
			}

			ghostdagData, err := s.ghostdagDataStores[0].Get(s.databaseContext, stagingArea, current, false)
			if database.IsNotFoundError(err) {
				break
			}
			if err != nil {
				return 0, err
			}
			selectedParent := ghostdagData.SelectedParent()
			if selectedParent == nil || selectedParent.Equal(model.VirtualGenesisBlockHash) {
				break
			}
			current = selectedParent
		}
	}

	if len(reset) == 0 {
		return 0, nil
	}
	if err := staging.CommitAllChanges(s.databaseContext, stagingArea); err != nil {
		return 0, err
	}
	return uint64(len(reset)), nil
}

// RepairMissingMultisets finds every StatusUTXOValid block reachable by walking each virtual tip's
// selected-parent chain that has no stored multiset - the exact state RepairBlockStatuses can leave
// behind (see RepairDisqualifiedTipChains's own comment: "which is how a repaired node ends up with
// UTXO-valid blocks that have no diff") - and marks each one StatusUTXOPendingVerification so the
// normal resolve path (getUnverifiedChainBlocks/ResolveVirtual) re-derives a real diff and multiset
// for it, exactly the way RepairDisqualifiedTipChains repairs a disqualified chain instead of
// inventing new multiset math here.
//
// A block only ever becomes StatusUTXOValid through the normal resolve path once its own multiset is
// staged (calculateMultiset requires its selected parent's multiset to succeed), so if a block on a
// branch already has a stored multiset, every earlier block on that same branch was resolved
// correctly before it and does not need checking - each branch's walk stops there.
//
// Without this repair, any consumer that needs a UTXOValid block's multiset (most importantly
// building a new block template over virtual, whose selected parent must be one) fails outright with
// "Multiset <hash> does not exist in db", which is fatal to producing any further blocks until fixed.
// It returns how many blocks it reset.
func (s *consensus) RepairMissingMultisets() (uint64, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	stagingArea := model.NewStagingArea()
	tips, err := s.consensusStateStore.Tips(stagingArea, s.databaseContext)
	if err != nil {
		return 0, err
	}

	reset := make(map[externalapi.DomainHash]struct{})
	for _, tip := range tips {
		current := tip
		for {
			if _, alreadyChecked := reset[*current]; alreadyChecked {
				break
			}

			status, err := s.blockStatusStore.Get(s.databaseContext, stagingArea, current)
			if database.IsNotFoundError(err) {
				break
			}
			if err != nil {
				return 0, err
			}
			if status != externalapi.StatusUTXOValid {
				// Anything else (already pending verification, disqualified, header-only) is
				// either already headed for the normal resolve path or is RepairDisqualifiedTipChains's
				// job, not this one's.
				break
			}

			_, msErr := s.multisetStore.Get(s.databaseContext, stagingArea, current)
			if msErr == nil {
				break
			}
			if !database.IsNotFoundError(msErr) {
				return 0, msErr
			}

			s.blockStatusStore.Stage(stagingArea, current, externalapi.StatusUTXOPendingVerification)
			reset[*current] = struct{}{}

			ghostdagData, err := s.ghostdagDataStores[0].Get(s.databaseContext, stagingArea, current, false)
			if database.IsNotFoundError(err) {
				break
			}
			if err != nil {
				return 0, err
			}
			selectedParent := ghostdagData.SelectedParent()
			if selectedParent == nil || selectedParent.Equal(model.VirtualGenesisBlockHash) {
				break
			}
			current = selectedParent
		}
	}

	if len(reset) == 0 {
		return 0, nil
	}
	if err := staging.CommitAllChanges(s.databaseContext, stagingArea); err != nil {
		return 0, err
	}
	return uint64(len(reset)), nil
}

// RepairBlockStatuses iterates through all blocks and sets them to StatusUTXOValid
// unless they are StatusInvalid. This is useful for repairing databases where blocks
// were incorrectly marked as disqualified.
func (s *consensus) RepairBlockStatuses() error {
	log.Info("Starting block status repair (setting all non-invalid blocks to StatusUTXOValid)...")

	s.lock.Lock()
	defer s.lock.Unlock()

	// Clear the block status cache to ensure we read from the database
	s.blockStatusStore.ClearCache()
	log.Info("Block status cache cleared")

	iterator, err := s.blockStore.AllBlockHashesIterator(s.databaseContext)
	if err != nil {
		return errors.Wrap(err, "failed to get block hashes iterator")
	}
	defer iterator.Close()

	var repairedCount int
	var totalCount int

	if !iterator.First() {
		log.Info("No blocks found in database")
		return nil
	}

	for {
		blockHash, err := iterator.Get()
		if err != nil {
			return errors.Wrap(err, "failed to get block hash")
		}

		totalCount++

		// Create a staging area for this block
		stagingArea := model.NewStagingArea()

		// Get the current status
		currentStatus, err := s.blockStatusStore.Get(s.databaseContext, stagingArea, blockHash)
		if err != nil {
			if database.IsNotFoundError(err) {
				// Block status not found - skip
				if !iterator.Next() {
					break
				}
				continue
			}
			return errors.Wrapf(err, "failed to get status for block %s", blockHash)
		}

		// Set to StatusUTXOValid unless it's StatusInvalid
		var newStatus externalapi.BlockStatus
		if currentStatus == externalapi.StatusInvalid {
			newStatus = externalapi.StatusInvalid
		} else if currentStatus == externalapi.StatusHeaderOnly {
			newStatus = externalapi.StatusHeaderOnly
		} else {
			newStatus = externalapi.StatusUTXOValid
		}

		// Only update if the status needs to change
		if newStatus != currentStatus {
			repairedCount++
			log.Debugf("Repairing block %s: status %d -> %d", blockHash, currentStatus, newStatus)

			// Create a staging area for the status update
			stagingAreaForStatus := model.NewStagingArea()
			s.blockStatusStore.Stage(stagingAreaForStatus, blockHash, newStatus)

			// Commit the repaired status
			if err := staging.CommitAllChanges(s.databaseContext, stagingAreaForStatus); err != nil {
				return errors.Wrapf(err, "failed to commit status remap for block %s", blockHash)
			}
		}

		// Log progress every 1000 blocks
		if totalCount%1000 == 0 {
			log.Infof("Processed %d blocks, repaired %d so far...",
				totalCount, repairedCount)
		}

		if !iterator.Next() {
			break
		}
	}

	log.Infof("Block status repair complete. Total blocks: %d, Repaired: %d",
		totalCount, repairedCount)
	return nil
}

// ReresolveInvalidBlocks iterates through all blocks with StatusInvalid (0) and re-resolves
// their status to ensure they are correctly classified. This is useful after migrating from
// an old schema where error states (1-3) were mapped to StatusInvalid (0), but some of those
// blocks might actually be valid under the new schema.
func (s *consensus) ReresolveInvalidBlocks() error {
	log.Info("Starting re-resolution of all StatusInvalid blocks...")

	s.lock.Lock()
	defer s.lock.Unlock()

	// Clear the block status cache to ensure we read from the database
	s.blockStatusStore.ClearCache()
	log.Info("Block status cache cleared")

	iterator, err := s.blockStore.AllBlockHashesIterator(s.databaseContext)
	if err != nil {
		return errors.Wrap(err, "failed to get block hashes iterator")
	}
	defer iterator.Close()

	var reresolvedCount int
	var updatedCount int
	var totalCount int

	if !iterator.First() {
		log.Info("No blocks found in database")
		return nil
	}

	for {
		blockHash, err := iterator.Get()
		if err != nil {
			return errors.Wrap(err, "failed to get block hash")
		}

		totalCount++

		// Create a staging area for this block
		stagingArea := model.NewStagingArea()

		// Get the current status
		currentStatus, err := s.blockStatusStore.Get(s.databaseContext, stagingArea, blockHash)
		if err != nil {
			if database.IsNotFoundError(err) {
				// Block status not found - skip
				if !iterator.Next() {
					break
				}
				continue
			}
			return errors.Wrapf(err, "failed to get status for block %s", blockHash)
		}

		// Process blocks with StatusInvalid or StatusDisqualifiedFromChain
		// that might need re-resolution after changes to consensus rules or state
		if currentStatus == externalapi.StatusInvalid || currentStatus == externalapi.StatusDisqualifiedFromChain {
			reresolvedCount++
			log.Debugf("Re-resolving block %s with status %s...", blockHash, currentStatus)

			// Create a new staging area for resolving
			stagingAreaForResolve := model.NewStagingArea()

			// Resolve the current status
			resolvedStatus, _, err := s.consensusStateManager.ResolveBlockStatus(
				stagingAreaForResolve, blockHash, true)
			if err != nil {
				log.Warnf("Failed to resolve status for block %s: %v", blockHash, err)
				// Skip this block but continue with others
				if !iterator.Next() {
					break
				}
				continue
			}

			// If the resolved status is different, update it
			if resolvedStatus != currentStatus {
				// Commit all changes (including the corrected block status and any related data)
				if err := staging.CommitAllChanges(s.databaseContext, stagingAreaForResolve); err != nil {
					return errors.Wrapf(err, "failed to commit status update for block %s", blockHash)
				}

				updatedCount++
				log.Infof("Updated block %s: status %d -> %d", blockHash, currentStatus, resolvedStatus)
			} else {
				log.Debugf("Block %s confirmed as StatusInvalid", blockHash)
			}
		}

		// Log progress every 1000 blocks
		if totalCount%1000 == 0 {
			log.Infof("Processed %d blocks, re-resolved %d blocks with invalid/disqualified status, updated %d so far...",
				totalCount, reresolvedCount, updatedCount)
		}

		if !iterator.Next() {
			break
		}
	}

	log.Infof("Re-resolution complete. Total blocks: %d, blocks with invalid/disqualified status re-resolved: %d, Updated: %d",
		totalCount, reresolvedCount, updatedCount)
	return nil
}

func (s *consensus) GetBlockByTransactionID(transactionID *externalapi.DomainTransactionID) (*externalapi.DomainBlock, error) {
	// Get an iterator to go through all blocks
	iterator, err := s.blockStore.AllBlockHashesIterator(s.databaseContext)
	if err != nil {
		return nil, err
	}
	defer iterator.Close()

	// Iterate through all blocks
	if iterator.First() {
		for {
			blockHash, err := iterator.Get()
			if err != nil {
				return nil, err
			}

			// Use a separate staging area for each block to avoid memory accumulation
			stagingArea := model.NewStagingArea()

			// Hold lock briefly for block retrieval
			s.lock.Lock()
			block, err := s.blockStore.Block(s.databaseContext, stagingArea, blockHash)
			s.lock.Unlock()
			if err != nil {
				// Skip blocks that can't be retrieved (might be pruned)
				if !iterator.Next() {
					break
				}
				continue
			}

			// Check if the transaction ID is in this block
			for _, tx := range block.Transactions {
				txID := consensushashing.TransactionID(tx)
				if txID.Equal(transactionID) {
					return block, nil
				}
			}

			if !iterator.Next() {
				break
			}
		}
	}

	return nil, errors.Wrapf(ErrTransactionNotInAnyBlock, "transaction %s is not in any block this node "+
		"holds", transactionID)
}

// ErrTransactionNotInAnyBlock is returned by GetBlockByTransactionID when the scan completed and no
// stored block contains the transaction, as opposed to the scan itself failing.
//
// Callers used to collapse both into "not found", which tells a caller two very different things
// with one answer: that this node has never seen the transaction, or that the node could not manage
// to look. The first is information about the transaction, the second is a fault in the node, and a
// wallet deciding whether to rebroadcast needs to tell them apart.
var ErrTransactionNotInAnyBlock = errors.New("transaction is not in any block this node holds")

// ValidateUTXODiffChildChains validates and repairs UTXO diff child chains
func (s *consensus) ValidateUTXODiffChildChains() error {
	// Don't hold the consensus lock during validation/repair as it can take several minutes
	// The validation logic only reads data and uses its own staging areas for commits
	return s.consensusStateManager.ValidateUTXODiffChildChains()
}

func (s *consensus) CheckMergeSetBluesAndIfBlockExistsInThem(searchedBlock *externalapi.DomainHash) error {
	log.Info("Starting CheckMergeSetBluesAndIfBlockExistsInThem ...")

	s.lock.Lock()
	defer s.lock.Unlock()

	iterator, err := s.blockStore.AllBlockHashesIterator(s.databaseContext)
	if err != nil {
		return errors.Wrap(err, "failed to get block hashes iterator")
	}
	defer iterator.Close()

	var totalCount int

	if !iterator.First() {
		log.Info("No blocks found in database")
		return nil
	}

	// Create a staging area for this block
	stagingArea := model.NewStagingArea()
	for {
		blockHash, err := iterator.Get()
		if err != nil {
			return errors.Wrap(err, "failed to get block hash")
		}

		status, err := s.blockStatusStore.Get(s.databaseContext, stagingArea, blockHash)
		if err != nil {
			return errors.Wrap(err, "failed to get block")
		}
		if status == externalapi.StatusHeaderOnly {
			if !iterator.Next() {
				break
			}
			continue
		}

		totalCount++

		for i := 0; i < len(s.ghostdagDataStores); i++ {
			if blockHash.Equal(searchedBlock) {
				log.Infof("Block found itself")
			}

			ghostDAGData, err := s.ghostdagDataStores[i].Get(s.databaseContext, stagingArea, blockHash, false)
			if err != nil {
				if !database.IsNotFoundError(err) {
					return errors.Wrapf(err, "failed to get GHOSTDAG data for block %s", blockHash)
				}
			}
			if ghostDAGData != nil {
				for _, blue := range ghostDAGData.MergeSetBlues() {
					if blue.Equal(searchedBlock) {
						log.Infof("Found the blockhash %s in mergeset blues of %s", searchedBlock, blockHash)
						break
					}
				}
				for _, red := range ghostDAGData.MergeSetReds() {
					if red.Equal(searchedBlock) {
						log.Infof("Found the blockhash %s in mergeset reds of %s", searchedBlock, blockHash)
						break
					}
				}
			}

			// Re-run GHOSTDAG to recalculate the data correctly
			err = s.ghostdagManagers[i].GHOSTDAG(stagingArea, blockHash)
			if err != nil {
				continue
			}

			ghostDAGData, err = s.ghostdagDataStores[i].Get(s.databaseContext, stagingArea, blockHash, false)
			if err != nil {
				if database.IsNotFoundError(err) {
					// GHOSTDAG data not found after recalculation - skip to next store
					continue
				}
				return errors.Wrapf(err, "failed to get GHOSTDAG data for block %s", blockHash)
			}
			if ghostDAGData != nil {
				for _, blue := range ghostDAGData.MergeSetBlues() {
					if blue.Equal(searchedBlock) {
						log.Infof("Found the blockhash %s in mergeset blues of %s", searchedBlock, blockHash)
						break
					}
				}
				for _, red := range ghostDAGData.MergeSetReds() {
					if red.Equal(searchedBlock) {
						log.Infof("Found the blockhash %s in mergeset reds of %s", searchedBlock, blockHash)
						break
					}
				}
			}

		}

		// Log progress every 1000 blocks
		if totalCount%1000 == 0 {
			log.Infof("Processed %d blocks..", totalCount)
		}

		if !iterator.Next() {
			break
		}
	}

	log.Infof("CheckMergeSetBluesAndIfBlockExistsInThem complete. Total blocks: %d", totalCount)
	return nil
}

// IterateUTXOSetAtBlockFromAcceptanceData streams the UTXO set as of blockHash the way the block
// headers' UTXO commitments define it: the pruning point's UTXO set with every accepted
// transaction between the pruning point and blockHash applied on top, taken from the acceptance
// data the node recorded when it resolved each of those blocks.
//
// This is deliberately a second, independent derivation from IterateUTXOSetAtBlock, which reads
// virtual's materialised UTXO table and walks the stored UTXO-diff chain back to blockHash. The
// two are meant to be the same set, and are not: the materialised table is maintained by applying
// UTXO diffs and never recomputed, so any diff that was mis-applied stays mis-applied forever,
// while the acceptance data is what the per-block multiset chain - the thing block headers
// actually commit to - is computed from. On a mainnet database idle since August the two differ
// by seven outpoints out of 10,150,996, including a spendable 629,814 HTN output the materialised
// table had lost.
//
// For anything whose purpose is to become a trusted floor - an exodus pruning point candidate
// above all - this is the derivation to use, because it is the one that reproduces header
// commitments.
//
// Memory is bounded by activity between the pruning point and blockHash (the outputs created and
// the outpoints spent in that window), not by the size of the UTXO set: entries surviving from the
// pruning point are streamed straight off disk.
//
// The whole iteration runs under the consensus lock, so callback must not call back into this
// Consensus instance.
func (s *consensus) IterateUTXOSetAtBlockFromAcceptanceData(blockHash *externalapi.DomainHash,
	callback func(outpoint *externalapi.DomainOutpoint, entry externalapi.UTXOEntry) error,
) error {
	s.lock.Lock()
	defer s.lock.Unlock()

	stagingArea := model.NewStagingArea()

	pruningPoint, err := s.pruningStore.PruningPoint(s.databaseContext, stagingArea)
	if err != nil {
		return err
	}

	// Collect the selected chain from blockHash down to the pruning point, so it can be applied
	// forwards. blockHash must be on the selected chain at or above the pruning point - anything
	// below it no longer has the acceptance data this derivation needs.
	var chain []*externalapi.DomainHash
	for current := blockHash; !current.Equal(pruningPoint); {
		chain = append(chain, current)
		blockGHOSTDAGData, err := s.ghostdagDataStores[0].Get(s.databaseContext, stagingArea, current, false)
		if err != nil {
			return errors.Wrapf(err, "failed to walk the selected chain from %s down to the pruning point "+
				"%s: no GHOSTDAG data for %s", blockHash, pruningPoint, current)
		}
		current = blockGHOSTDAGData.SelectedParent()
		if current == nil || current.Equal(model.VirtualGenesisBlockHash) {
			return errors.Errorf("block %s is not a selected chain block at or above the pruning point %s, "+
				"so its UTXO set cannot be derived from acceptance data", blockHash, pruningPoint)
		}
	}

	// Apply the window forwards, tracking only what it changes.
	created := make(map[externalapi.DomainOutpoint]externalapi.UTXOEntry)
	spentFromPruningPointSet := make(map[externalapi.DomainOutpoint]struct{})
	for i := len(chain) - 1; i >= 0; i-- {
		mergingBlockHash := chain[i]
		// Every UTXO an accepted transaction creates is stamped with the DAA score of the block that
		// merged it - see utxo.AcceptedUTXOBlockDAAScore. Stamping anything else here would produce a
		// set that serializes differently from the one the headers commit to, even where every
		// outpoint agrees.
		mergingBlockDAAScore, err := s.blockOwnDAAScore(stagingArea, mergingBlockHash)
		if err != nil {
			return err
		}
		acceptanceData, err := s.acceptanceDataStore.Get(s.databaseContext, stagingArea, mergingBlockHash)
		if err != nil {
			return errors.Wrapf(err, "no acceptance data for chain block %s, so the UTXO set of %s cannot "+
				"be derived from acceptance data", mergingBlockHash, blockHash)
		}
		for _, blockAcceptanceData := range acceptanceData {
			for j, transactionAcceptanceData := range blockAcceptanceData.TransactionAcceptanceData {
				if !transactionAcceptanceData.IsAccepted {
					continue
				}
				transaction := transactionAcceptanceData.Transaction
				for _, input := range transaction.Inputs {
					if _, createdInWindow := created[input.PreviousOutpoint]; createdInWindow {
						delete(created, input.PreviousOutpoint)
						continue
					}
					spentFromPruningPointSet[input.PreviousOutpoint] = struct{}{}
				}
				transactionID := *consensushashing.TransactionID(transaction)
				for outputIndex, output := range transaction.Outputs {
					if outputIndex > math.MaxUint32 {
						return errors.Errorf("output index %d cannot be represented as uint32", outputIndex)
					}
					outpoint := externalapi.DomainOutpoint{TransactionID: transactionID, Index: uint32(outputIndex)}
					delete(spentFromPruningPointSet, outpoint)
					// One definition of coinbase-ness, shared with the multiset and the diff - see
					// utxo.IsAcceptedCoinbase. This derivation is what an exodus bundle is built from, so
					// a coin stamped differently here than in the set the chain committed to would be
					// baked into the floor every node is asked to adopt.
					created[outpoint] = utxo.NewUTXOEntry(output.Value, output.ScriptPublicKey,
						utxo.IsAcceptedCoinbase(transaction, j), mergingBlockDAAScore)
				}
			}
		}
	}

	pruningPointUTXOIterator, err := s.pruningStore.PruningPointUTXOIterator(s.databaseContext)
	if err != nil {
		return err
	}
	defer pruningPointUTXOIterator.Close()

	for ok := pruningPointUTXOIterator.First(); ok; ok = pruningPointUTXOIterator.Next() {
		outpoint, entry, err := pruningPointUTXOIterator.Get()
		if err != nil {
			return err
		}
		if _, spent := spentFromPruningPointSet[*outpoint]; spent {
			continue
		}
		// An outpoint the window re-created shadows the pruning point set's copy of it.
		if windowEntry, ok := created[*outpoint]; ok {
			err = callback(outpoint, windowEntry)
			if err != nil {
				return err
			}
			delete(created, *outpoint)
			continue
		}
		err = callback(outpoint, entry)
		if err != nil {
			return err
		}
	}

	for outpoint, entry := range created {
		outpointCopy := outpoint
		err = callback(&outpointCopy, entry)
		if err != nil {
			return err
		}
	}

	return nil
}

// blockOwnDAAScore returns blockHash's own DAA score, preferring its header - the same lookup
// consensusstatemanager uses when stamping the UTXO entries a block's resolution creates.
func (s *consensus) blockOwnDAAScore(stagingArea *model.StagingArea,
	blockHash *externalapi.DomainHash,
) (uint64, error) {
	header, err := s.blockHeaderStore.BlockHeader(s.databaseContext, stagingArea, blockHash)
	if err != nil {
		return s.daaBlocksStore.DAAScore(s.databaseContext, stagingArea, blockHash)
	}
	return header.DAAScore(), nil
}
