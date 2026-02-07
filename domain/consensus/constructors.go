package consensus

import (
	"math/big"
	"time"

	"github.com/Hoosat-Oy/HTND/domain/consensus/model"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
)

// GHOSTDAGManagerConstructor is the function signature for a constructor of a type implementing model.GHOSTDAGManager
type GHOSTDAGManagerConstructor func(
	databaseContext model.DBReader,
	dagTopologyManager model.DAGTopologyManager,
	dagTraversalManager model.DAGTraversalManager,
	ghostdagDataStore model.GHOSTDAGDataStore,
	headerStore model.BlockHeaderStore,
	consensusStateStore model.ConsensusStateStore,
	k []externalapi.KType,
	genesisHash *externalapi.DomainHash) model.GHOSTDAGManager

// DifficultyManagerConstructor is the function signature for a constructor of a type implementing model.DifficultyManager
type DifficultyManagerConstructor func(model.DBReader, model.GHOSTDAGManager, model.GHOSTDAGDataStore,
	model.BlockHeaderStore, model.DAABlocksStore, model.DAGTopologyManager, model.DAGTraversalManager, *big.Int, []int, bool, []time.Duration,
	*externalapi.DomainHash, uint32) model.DifficultyManager

// PastMedianTimeManagerConstructor is the function signature for a constructor of a type implementing model.PastMedianTimeManager
type PastMedianTimeManagerConstructor func(int, model.DBReader, model.DAGTraversalManager, model.BlockHeaderStore,
	model.GHOSTDAGDataStore, *externalapi.DomainHash) model.PastMedianTimeManager
