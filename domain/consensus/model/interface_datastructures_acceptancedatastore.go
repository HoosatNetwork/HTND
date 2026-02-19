package model

import "github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"

// AcceptanceDataStore represents a store of AcceptanceData
type AcceptanceDataStore interface {
	Store
	Stage(stagingArea *StagingArea, blockHash *externalapi.DomainHash, acceptanceData externalapi.AcceptanceData)
	IsStaged(stagingArea *StagingArea) bool
	UnstageAll(stagingArea *StagingArea)
	Get(dbContext DBReader, stagingArea *StagingArea, blockHash *externalapi.DomainHash) (externalapi.AcceptanceData, error)
	Delete(stagingArea *StagingArea, blockHash *externalapi.DomainHash)
	CacheLen() int
}
