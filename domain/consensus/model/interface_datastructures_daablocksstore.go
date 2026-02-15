package model

import "github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"

// DAABlocksStore represents a store of ???
type DAABlocksStore interface {
	Store
	StageDAAScore(stagingArea *StagingArea, blockHash *externalapi.DomainHash, daaScore uint64)
	StageBlockDAAAddedBlocks(stagingArea *StagingArea, blockHash *externalapi.DomainHash, addedBlocks []*externalapi.DomainHash)
	IsStaged(stagingArea *StagingArea) bool
	UnstageAll(stagingArea *StagingArea)
	DAAAddedBlocks(dbContext DBReader, stagingArea *StagingArea, blockHash *externalapi.DomainHash) ([]*externalapi.DomainHash, error)
	DAAScore(dbContext DBReader, stagingArea *StagingArea, blockHash *externalapi.DomainHash) (uint64, error)
	Delete(stagingArea *StagingArea, blockHash *externalapi.DomainHash)
	CacheLen() int
}
