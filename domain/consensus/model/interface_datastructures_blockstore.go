package model

import "github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"

// BlockStore represents a store of blocks
type BlockStore interface {
	Stage(stagingArea *StagingArea, blockHash *externalapi.DomainHash, block *externalapi.DomainBlock)
	IsStaged(stagingArea *StagingArea) bool
	UnstageAll(stagingArea *StagingArea)
	Block(dbContext DBReader, stagingArea *StagingArea, blockHash *externalapi.DomainHash) (*externalapi.DomainBlock, error)
	// BlockWithoutCaching reads a committed block without adding it to the cache, for callers that do
	// not hold the consensus lock.
	BlockWithoutCaching(dbContext DBReader, blockHash *externalapi.DomainHash) (*externalapi.DomainBlock, error)
	HasBlock(dbContext DBReader, stagingArea *StagingArea, blockHash *externalapi.DomainHash) (bool, error)
	// HasBlockWithoutCaching reports whether a committed block exists without remembering the answer,
	// for callers that do not hold the consensus lock.
	HasBlockWithoutCaching(dbContext DBReader, blockHash *externalapi.DomainHash) (bool, error)
	Blocks(dbContext DBReader, stagingArea *StagingArea, blockHashes []*externalapi.DomainHash) ([]*externalapi.DomainBlock, error)
	Delete(stagingArea *StagingArea, blockHash *externalapi.DomainHash)
	Count(stagingArea *StagingArea) uint64
	AllBlockHashesIterator(dbContext DBReader) (BlockIterator, error)
	CacheLen() int
}
