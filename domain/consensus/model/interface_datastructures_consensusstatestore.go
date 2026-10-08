package model

import "github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"

// OrderedVirtualUTXOKey is one outpoint encoded as a virtual-UTXO-set database key.
// Index is that outpoint's position in the slice passed to PrepareOrderedVirtualUTXOKeys.
// Key aliases memory owned by the returned slice and stays valid while that slice is reachable.
type OrderedVirtualUTXOKey struct {
	Index int
	Key   []byte
}

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
	// UTXOsByOutpointsWithoutPopulatingCache sets entries[i] to what UTXOByOutpointWithoutPopulatingCache
	// would answer for outpoints[i], or nil where the coin does not exist, reading the database in key
	// order rather than one Get per outpoint.
	UTXOsByOutpointsWithoutPopulatingCache(dbContext DBReader, stagingArea *StagingArea,
		outpoints []*externalapi.DomainOutpoint, entries []externalapi.UTXOEntry) error
	// PrepareOrderedVirtualUTXOKeys encodes outpoints as virtual-UTXO-set keys and sorts them by
	// those keys. It does not read the database, so a caller may run it outside the consensus lock.
	PrepareOrderedVirtualUTXOKeys(outpoints []*externalapi.DomainOutpoint) ([]OrderedVirtualUTXOKey, error)
	// UTXOsByOrderedKeysWithoutPopulatingCache fills entries from ordered, which must be a key-sorted
	// slice produced by PrepareOrderedVirtualUTXOKeys for these outpoints. entries[ordered[i].Index]
	// is set to the coin, or nil where virtual does not hold it. When preferred is non-nil,
	// preferred[i] is indexed like entries: a preferred entry whose serialization equals the stored
	// bytes is returned as itself instead of being decoded again.
	UTXOsByOrderedKeysWithoutPopulatingCache(dbContext DBReader, stagingArea *StagingArea,
		outpoints []*externalapi.DomainOutpoint, ordered []OrderedVirtualUTXOKey,
		entries []externalapi.UTXOEntry, preferred []externalapi.UTXOEntry) error
	// LookupUTXOByOutpoint returns the coin's entry and true, or nil and false where virtual's UTXO set
	// does not hold it - HasUTXOByOutpoint and UTXOByOutpoint in one lookup that consults the cache.
	// Only a database fault is an error.
	LookupUTXOByOutpoint(dbContext DBReader, stagingArea *StagingArea, outpoint *externalapi.DomainOutpoint) (externalapi.UTXOEntry, bool, error)
	// LookupUTXOByOutpointWithoutPopulatingCache is LookupUTXOByOutpoint without adding a miss to the cache.
	LookupUTXOByOutpointWithoutPopulatingCache(dbContext DBReader, stagingArea *StagingArea, outpoint *externalapi.DomainOutpoint) (externalapi.UTXOEntry, bool, error)
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
