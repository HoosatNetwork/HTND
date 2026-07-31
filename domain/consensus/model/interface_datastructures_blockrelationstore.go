package model

import "github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"

// BlockRelationStore represents a store of BlockRelations
type BlockRelationStore interface {
	StageBlockRelation(stagingArea *StagingArea, blockHash *externalapi.DomainHash, blockRelations *BlockRelations)
	IsStaged(stagingArea *StagingArea) bool
	BlockRelation(dbContext DBReader, stagingArea *StagingArea, blockHash *externalapi.DomainHash) (*BlockRelations, error)
	Has(dbContext DBReader, stagingArea *StagingArea, blockHash *externalapi.DomainHash) (bool, error)
	UnstageAll(stagingArea *StagingArea)
	CacheLen() int
}
