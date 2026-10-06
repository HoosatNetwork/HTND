package model

import "github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"

// MultisetStore represents a store of Multisets
type MultisetStore interface {
	Stage(stagingArea *StagingArea, blockHash *externalapi.DomainHash, multiset Multiset)
	IsStaged(stagingArea *StagingArea) bool
	UnstageAll(stagingArea *StagingArea)
	Get(dbContext DBReader, stagingArea *StagingArea, blockHash *externalapi.DomainHash) (Multiset, error)
	Delete(stagingArea *StagingArea, blockHash *externalapi.DomainHash)
	CacheLen() int
}
