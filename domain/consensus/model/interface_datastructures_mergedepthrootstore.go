package model

import (
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
)

// MergeDepthRootStore represents a store for merge depth roots
type MergeDepthRootStore interface {
	IsStaged(stagingArea *StagingArea) bool
	StageMergeDepthRoot(stagingArea *StagingArea, blockHash *externalapi.DomainHash, root *externalapi.DomainHash)
	MergeDepthRoot(dbContext DBReader, stagingArea *StagingArea, blockHash *externalapi.DomainHash) (*externalapi.DomainHash, error)
	CacheLen() int
	UnstageAll(stagingArea *StagingArea)
}
