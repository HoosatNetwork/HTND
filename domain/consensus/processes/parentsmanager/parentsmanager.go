package parentssanager

import (
	"github.com/Hoosat-Oy/HTND/domain/consensus/model"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
)

type parentsManager struct {
	genesisHash   *externalapi.DomainHash
	maxBlockLevel int
}

// New instantiates a new ParentsManager
func New(genesisHash *externalapi.DomainHash, maxBlockLevel int) model.ParentsManager {
	return &parentsManager{
		genesisHash:   genesisHash,
		maxBlockLevel: maxBlockLevel,
	}
}

func (pm *parentsManager) ParentsAtLevel(blockHeader externalapi.BlockHeader, level int) externalapi.BlockLevelParents {
	var parentsAtLevel externalapi.BlockLevelParents
	if len(blockHeader.Parents()) > level {
		parentsAtLevel = blockHeader.Parents()[level]
	}

	if len(parentsAtLevel) == 0 && len(blockHeader.DirectParents()) > 0 {
		return externalapi.BlockLevelParents{pm.genesisHash}
	}

	return parentsAtLevel
}

func (pm *parentsManager) Parents(blockHeader externalapi.BlockHeader) []externalapi.BlockLevelParents {
	numParents := pm.maxBlockLevel + 1
	parents := make([]externalapi.BlockLevelParents, numParents)
	for i := range numParents {
		parents[i] = pm.ParentsAtLevel(blockHeader, i)
	}

	return parents
}
