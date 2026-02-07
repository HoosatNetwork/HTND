package ghostdagmanager

import (
	"github.com/Hoosat-Oy/HTND/domain/consensus/model"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
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
	}
}

// SetDAGTraversalManager sets the DAG traversal manager for this ghostdag manager
func (gm *ghostdagManager) SetDAGTraversalManager(dagTraversalManager model.DAGTraversalManager) {
	gm.dagTraversalManager = dagTraversalManager
}
