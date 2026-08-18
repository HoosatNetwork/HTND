package ghostdagmanager

import (
	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/lrucache"
)

// ghostdagManager resolves and manages GHOSTDAG block data
type ghostdagManager struct {
	databaseContext     model.DBReader
	dagTopologyManager  model.DAGTopologyManager
	dagTraversalManager model.DAGTraversalManager
	ghostdagDataStore   model.GHOSTDAGDataStore
	headerStore         model.BlockHeaderStore
	consensusStateStore model.ConsensusStateStore

	k           []externalapi.KType
	genesisHash *externalapi.DomainHash

	// Cache only UMCVoting results (DAGKnight)
	umcVotingCache *lrucache.LRUCache[int]
	orderDAGCache  *lrucache.LRUCache[orderDAGResult]
	chainPathCache *lrucache.LRUCache[[]*externalapi.DomainHash]
	lcaCache       *lrucache.LRUCache[*externalapi.DomainHash]
	agreesCache    *lrucache.LRUCache[bool]
}

// New instantiates a new GHOSTDAGManager
func New(
	databaseContext model.DBReader,
	dagTopologyManager model.DAGTopologyManager,
	dagTraversalManager model.DAGTraversalManager,
	ghostdagDataStore model.GHOSTDAGDataStore,
	headerStore model.BlockHeaderStore,
	consensusStateStore model.ConsensusStateStore,
	k []externalapi.KType,
	genesisHash *externalapi.DomainHash,
) model.GHOSTDAGManager {
	return &ghostdagManager{
		databaseContext:     databaseContext,
		dagTopologyManager:  dagTopologyManager,
		dagTraversalManager: dagTraversalManager,
		ghostdagDataStore:   ghostdagDataStore,
		headerStore:         headerStore,
		consensusStateStore: consensusStateStore,
		k:                   k,
		genesisHash:         genesisHash,
		umcVotingCache:      lrucache.New[int](500, true),
		orderDAGCache:       lrucache.New[orderDAGResult](500, true),
		chainPathCache:      lrucache.New[[]*externalapi.DomainHash](2000, true),
		lcaCache:            lrucache.New[*externalapi.DomainHash](5000, true),
		agreesCache:         lrucache.New[bool](10000, true),
	}
}

// SetDAGTraversalManager sets the DAG traversal manager for this ghostdag manager
func (gm *ghostdagManager) SetDAGTraversalManager(dagTraversalManager model.DAGTraversalManager) {
	gm.dagTraversalManager = dagTraversalManager
}
