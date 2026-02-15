package model

import "github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"

// HeadersSelectedChainStore represents a store of the headers selected chain
type HeadersSelectedChainStore interface {
	Store
	Stage(dbContext DBReader, stagingArea *StagingArea, chainChanges *externalapi.SelectedChainPath) error
	IsStaged(stagingArea *StagingArea) bool
	UnstageAll(stagingArea *StagingArea)
	GetIndexByHash(dbContext DBReader, stagingArea *StagingArea, blockHash *externalapi.DomainHash) (uint64, error)
	GetHashByIndex(dbContext DBReader, stagingArea *StagingArea, index uint64) (*externalapi.DomainHash, error)
	CacheLen() int
}
