package model

import "github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"

// BlockStatusStore represents a store of BlockStatuses
type BlockStatusStore interface {
	Stage(stagingArea *StagingArea, blockHash *externalapi.DomainHash, blockStatus externalapi.BlockStatus)
	IsStaged(stagingArea *StagingArea) bool
	Get(dbContext DBReader, stagingArea *StagingArea, blockHash *externalapi.DomainHash) (externalapi.BlockStatus, error)
	// GetWithoutCaching reads a block's last committed status from the database, bypassing the cache,
	// for callers that do not hold the consensus lock.
	GetWithoutCaching(dbContext DBReader, blockHash *externalapi.DomainHash) (status externalapi.BlockStatus, exists bool, err error)
	Exists(dbContext DBReader, stagingArea *StagingArea, blockHash *externalapi.DomainHash) (bool, error)
	UnstageAll(stagingArea *StagingArea)
	CacheLen() int
	ClearCache()
}
