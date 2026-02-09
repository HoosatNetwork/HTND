package ghostdagmanager

import (
	"github.com/Hoosat-Oy/HTND/domain/consensus/model"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/lrucache"
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

	// LRU caches for performance
	pastCache       *lrucache.LRUCache[[]*externalapi.DomainHash]
	futureCache     *lrucache.LRUCache[[]*externalapi.DomainHash]
	anticoneCache   *lrucache.LRUCache[[]*externalapi.DomainHash]
	kColouringCache *lrucache.LRUCache[KColouringResult]
	umcVotingCache  *lrucache.LRUCache[int]
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
	genesisHash *externalapi.DomainHash) model.GHOSTDAGManager {
	return &ghostdagManager{
		databaseContext:     databaseContext,
		dagTopologyManager:  dagTopologyManager,
		dagTraversalManager: dagTraversalManager,
		ghostdagDataStore:   ghostdagDataStore,
		headerStore:         headerStore,
		consensusStateStore: consensusStateStore,
		k:                   k,
		genesisHash:         genesisHash,
		pastCache:           lrucache.New[[]*externalapi.DomainHash](1000, true),
		futureCache:         lrucache.New[[]*externalapi.DomainHash](1000, true),
		anticoneCache:       lrucache.New[[]*externalapi.DomainHash](1000, true),
		kColouringCache:     lrucache.New[KColouringResult](500, true),
		umcVotingCache:      lrucache.New[int](500, true),
	}
}

// SetDAGTraversalManager sets the DAG traversal manager for this ghostdag manager
func (gm *ghostdagManager) SetDAGTraversalManager(dagTraversalManager model.DAGTraversalManager) {
	gm.dagTraversalManager = dagTraversalManager
}
