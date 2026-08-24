package consensus

import (
	"math/big"
	"os"
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
	"github.com/HoosatNetwork/HTND/infrastructure/logger"
	"github.com/HoosatNetwork/HTND/util/staging"
	"github.com/pkg/errors"
)

type consensus struct {
	lock            *sync.Mutex
	databaseContext model.DBManager

	genesisBlock *externalapi.DomainBlock
	genesisHash  *externalapi.DomainHash

	expectedDAAWindowDurationInMilliseconds int64

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

	err = s.sendBlockAddedEvent(block, blockStatus)
	if err != nil {
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

func (s *consensus) sendVirtualChangedEvent(virtualChangeSet *externalapi.VirtualChangeSet, wasVirtualUpdated bool) error {
	if !wasVirtualUpdated || s.consensusEventsChan == nil || virtualChangeSet == nil {
		return nil
	}

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

	s.consensusEventsChan <- virtualChangeSet
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

	pruningPointUTXOs, err := s.pruningStore.PruningPointUTXOs(s.databaseContext, fromOutpoint, limit)
	if err != nil {
		return nil, err
	}
	return pruningPointUTXOs, nil
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
		return errors.Errorf(
			"ResolveVirtual finished with no UTXO-valid/pending tip (all tips disqualified or invalid); virtual cannot leave VirtualGenesis")
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

	// GHOSTDAG data not found in store, try to get it from blocksWithTrustedDataDAAWindowStore
	ghostdagDataHashPair, err := s.blocksWithTrustedDataDAAWindowStore.DAAWindowBlock(s.databaseContext, stagingArea, trustedBlockHash, daaBlockWindowIndex)
	if err != nil {
		log.Infof("TrustedDataDataDAAHeader failed to retrieve with %s\n", daaBlockHash)
		return nil, err
	}

	return &externalapi.TrustedDataDataDAAHeader{
		Header:       header,
		GHOSTDAGData: ghostdagDataHashPair.GHOSTDAGData,
	}, nil
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
	// As a heuristic, we allow the node to mine if he is likely to be within the current DAA window of fully synced nodes.
	// Such blocks contribute to security by maintaining the current difficulty despite possibly being slightly out of sync.
	if now-virtualSelectedParentHeader.TimeInMilliseconds() < s.expectedDAAWindowDurationInMilliseconds {
		log.Debugf("The selected tip timestamp is recent (%d), current (%d), as limit (%d) so IsNearlySynced returns true",
			virtualSelectedParentHeader.TimeInMilliseconds(), now, s.expectedDAAWindowDurationInMilliseconds)
		return true, nil
	}

	log.Debugf("The selected tip timestamp is old (%d), current (%d), as limit (%d) so IsNearlySynced returns false",
		virtualSelectedParentHeader.TimeInMilliseconds(), now, s.expectedDAAWindowDurationInMilliseconds)
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

	return nil, errors.New("Transaction not found in any block")
}

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
