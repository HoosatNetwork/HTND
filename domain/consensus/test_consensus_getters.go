package consensus

import (
	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/testapi"
	"github.com/HoosatNetwork/HTND/infrastructure/db/database"
)

func (tc *testConsensus) DatabaseContext() model.DBManager {
	return tc.databaseContext
}

func (tc *testConsensus) Database() database.Database {
	return tc.database
}

func (tc *testConsensus) AcceptanceDataStore() model.AcceptanceDataStore {
	return tc.acceptanceDataStore
}

func (tc *testConsensus) BlockHeaderStore() model.BlockHeaderStore {
	return tc.blockHeaderStore
}

func (tc *testConsensus) BlockRelationStore() model.BlockRelationStore {
	return tc.blockRelationStores[0]
}

func (tc *testConsensus) BlockStatusStore() model.BlockStatusStore {
	return tc.blockStatusStore
}

func (tc *testConsensus) BlockStore() model.BlockStore {
	return tc.blockStore
}

func (tc *testConsensus) ConsensusStateStore() model.ConsensusStateStore {
	return tc.consensusStateStore
}

func (tc *testConsensus) GHOSTDAGDataStore() model.GHOSTDAGDataStore {
	return tc.ghostdagDataStores[0]
}

func (tc *testConsensus) GHOSTDAGDataStores() []model.GHOSTDAGDataStore {
	return tc.ghostdagDataStores
}

func (tc *testConsensus) HeaderTipsStore() model.HeaderSelectedTipStore {
	return tc.headersSelectedTipStore
}

func (tc *testConsensus) MultisetStore() model.MultisetStore {
	return tc.multisetStore
}

func (tc *testConsensus) PruningStore() model.PruningStore {
	return tc.pruningStore
}

func (tc *testConsensus) ReachabilityDataStore() model.ReachabilityDataStore {
	return tc.reachabilityDataStore
}

func (tc *testConsensus) UTXODiffStore() model.UTXODiffStore {
	return tc.utxoDiffStore
}

func (tc *testConsensus) BlockBuilder() testapi.TestBlockBuilder {
	return tc.testBlockBuilder
}

func (tc *testConsensus) BlockProcessor() model.BlockProcessor {
	return tc.blockProcessor
}

func (tc *testConsensus) BlockValidator() model.BlockValidator {
	return tc.blockValidator
}

func (tc *testConsensus) CoinbaseManager() model.CoinbaseManager {
	return tc.coinbaseManager
}

func (tc *testConsensus) ConsensusStateManager() testapi.TestConsensusStateManager {
	return tc.testConsensusStateManager
}

func (tc *testConsensus) DAGTopologyManager() model.DAGTopologyManager {
	return tc.dagTopologyManagers[0]
}

func (tc *testConsensus) DAGTraversalManager() model.DAGTraversalManager {
	return tc.dagTraversalManager
}

func (tc *testConsensus) DifficultyManager() model.DifficultyManager {
	return tc.difficultyManager
}

func (tc *testConsensus) GHOSTDAGManager() model.GHOSTDAGManager {
	return tc.ghostdagManagers[0]
}

func (tc *testConsensus) HeaderTipsManager() model.HeadersSelectedTipManager {
	return tc.headerTipsManager
}

func (tc *testConsensus) MergeDepthManager() model.MergeDepthManager {
	return tc.mergeDepthManager
}

func (tc *testConsensus) PastMedianTimeManager() model.PastMedianTimeManager {
	return tc.pastMedianTimeManager
}

func (tc *testConsensus) PruningManager() model.PruningManager {
	return tc.pruningManager
}

func (tc *testConsensus) ReachabilityManager() testapi.TestReachabilityManager {
	return tc.testReachabilityManager
}

func (tc *testConsensus) SyncManager() model.SyncManager {
	return tc.syncManager
}

func (tc *testConsensus) TransactionValidator() testapi.TestTransactionValidator {
	return tc.testTransactionValidator
}

func (tc *testConsensus) FinalityManager() model.FinalityManager {
	return tc.finalityManager
}

func (tc *testConsensus) FinalityStore() model.FinalityStore {
	return tc.finalityStore
}

func (tc *testConsensus) HeadersSelectedChainStore() model.HeadersSelectedChainStore {
	return tc.headersSelectedChainStore
}

func (tc *testConsensus) DAABlocksStore() model.DAABlocksStore {
	return tc.daaBlocksStore
}

func (tc *testConsensus) Consensus() externalapi.Consensus {
	return tc
}

// BlocksWithTrustedDataDAAWindowStore exposes the trusted DAA window store so a test can stage the
// shape validateAndInsertBlockWithTrustedData leaves a pruning point in: GHOSTDAG selected parent
// replaced by the virtual-genesis marker, and the real window present only as trusted data.
//
// Test-only. Nothing in production needs this store from outside consensus, and HTN-204 could not be
// regression-tested without it.
func (tc *testConsensus) BlocksWithTrustedDataDAAWindowStore() model.BlocksWithTrustedDataDAAWindowStore {
	return tc.blocksWithTrustedDataDAAWindowStore
}
