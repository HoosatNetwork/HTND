package model

import "github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"

// ConsensusStateStore represents a store for the current consensus state
type ConsensusStateStore interface {
	IsStaged(stagingArea *StagingArea) bool
	UnstageAll(stagingArea *StagingArea)

	StageVirtualUTXODiff(stagingArea *StagingArea, virtualUTXODiff externalapi.UTXODiff)
	UTXOByOutpoint(dbContext DBReader, stagingArea *StagingArea, outpoint *externalapi.DomainOutpoint) (externalapi.UTXOEntry, bool, error)
	// UTXOByOutpointWithoutPopulatingCache behaves like UTXOByOutpoint but never adds a cache miss to
	// the shared virtual UTXO cache - only a hit refreshes an entry's recency. For a bulk caller whose
	// outpoint count can dwarf the cache size, see HTN-207.
	UTXOByOutpointWithoutPopulatingCache(dbContext DBReader, stagingArea *StagingArea, outpoint *externalapi.DomainOutpoint) (externalapi.UTXOEntry, bool, error)
	HasUTXOByOutpoint(dbContext DBReader, stagingArea *StagingArea, outpoint *externalapi.DomainOutpoint) (bool, error)
	VirtualUTXOSetIterator(dbContext DBReader, stagingArea *StagingArea) (externalapi.ReadOnlyUTXOSetIterator, error)
	VirtualUTXOs(dbContext DBReader, fromOutpoint *externalapi.DomainOutpoint, limit int) ([]*externalapi.OutpointAndUTXOEntryPair, error)

	StageTips(stagingArea *StagingArea, tipHashes []*externalapi.DomainHash)
	Tips(stagingArea *StagingArea, dbContext DBReader) ([]*externalapi.DomainHash, error)

	StartImportingPruningPointUTXOSet(dbContext DBWriter) error
	HadStartedImportingPruningPointUTXOSet(dbContext DBWriter) (bool, error)
	ImportPruningPointUTXOSetIntoVirtualUTXOSet(dbContext DBWriter, pruningPointUTXOSetIterator externalapi.ReadOnlyUTXOSetIterator) error
	FinishImportingPruningPointUTXOSet(dbContext DBWriter) error
	CacheLen() int
}
