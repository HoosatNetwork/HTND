package model

import (
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
)

// FinalityStore represents a store for finality data
type FinalityStore interface {
	Store
	IsStaged(stagingArea *StagingArea) bool
	UnstageAll(stagingArea *StagingArea)
	StageFinalityPoint(stagingArea *StagingArea, blockHash *externalapi.DomainHash, finalityPointHash *externalapi.DomainHash)
	FinalityPoint(dbContext DBReader, stagingArea *StagingArea, blockHash *externalapi.DomainHash) (*externalapi.DomainHash, error)
	CacheLen() int
}
