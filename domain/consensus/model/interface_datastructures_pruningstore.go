package model

import (
	"time"

	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
)

// PruningStore represents a store for the current pruning state
type PruningStore interface {
	StagePruningPoint(dbContext DBWriter, stagingArea *StagingArea, pruningPointBlockHash *externalapi.DomainHash) error
	StagePruningPointByIndex(dbContext DBReader, stagingArea *StagingArea,
		pruningPointBlockHash *externalapi.DomainHash, index uint64) error
	StagePruningPointCandidate(stagingArea *StagingArea, candidate *externalapi.DomainHash)
	IsStaged(stagingArea *StagingArea) bool
	PruningPointCandidate(dbContext DBReader, stagingArea *StagingArea) (*externalapi.DomainHash, error)
	HasPruningPointCandidate(dbContext DBReader, stagingArea *StagingArea) (bool, error)
	PruningPoint(dbContext DBReader, stagingArea *StagingArea) (*externalapi.DomainHash, error)
	HasPruningPoint(dbContext DBReader, stagingArea *StagingArea) (bool, error)
	CurrentPruningPointIndex(dbContext DBReader, stagingArea *StagingArea) (uint64, error)
	PruningPointByIndex(dbContext DBReader, stagingArea *StagingArea, index uint64) (*externalapi.DomainHash, error)

	StageStartUpdatingPruningPointUTXOSet(stagingArea *StagingArea)
	HadStartedUpdatingPruningPointUTXOSet(dbContext DBWriter) (bool, error)
	FinishUpdatingPruningPointUTXOSet(dbContext DBWriter) error
	UpdatePruningPointUTXOSet(dbContext DBWriter, diff externalapi.UTXODiff) error

	ClearImportedPruningPointUTXOs(dbContext DBWriter) error
	AppendImportedPruningPointUTXOs(dbTx DBTransaction, outpointAndUTXOEntryPairs []*externalapi.OutpointAndUTXOEntryPair) error
	ImportedPruningPointUTXOIterator(dbContext DBReader) (externalapi.ReadOnlyUTXOSetIterator, error)
	ClearImportedPruningPointMultiset(dbContext DBWriter) error
	ImportedPruningPointMultiset(dbContext DBReader) (Multiset, error)
	UpdateImportedPruningPointMultiset(dbTx DBTransaction, multiset Multiset) error
	CommitImportedPruningPointUTXOSet(dbContext DBWriter) error
	PruningPointUTXOs(dbContext DBReader, fromOutpoint *externalapi.DomainOutpoint, limit int) ([]*externalapi.OutpointAndUTXOEntryPair, error)
	PruningPointUTXOIterator(dbContext DBReader) (externalapi.ReadOnlyUTXOSetIterator, error)

	StageLastPruningTime(stagingArea *StagingArea, lastPruningTime time.Time)
	LastPruningTime(dbContext DBReader) (time.Time, error)

	// SetLastUTXODebugCheckedPruningPoint/LastUTXODebugCheckedPruningPoint and
	// SetLastUTXODebugReproducedRootHash/LastUTXODebugReproducedRootHash persist debug-diagnostic
	// progress markers (see pruning_store.go's comments) so --enable-utxo-debug-diagnostics's
	// expensive startup checks can skip re-running when the underlying data hasn't changed since the
	// last boot. Not consensus-critical state - written directly, no staging area.
	SetLastUTXODebugCheckedPruningPoint(dbContext DBWriter, pruningPoint *externalapi.DomainHash) error
	LastUTXODebugCheckedPruningPoint(dbContext DBReader) (*externalapi.DomainHash, error)
	SetLastUTXODebugReproducedRootHash(dbContext DBWriter, rootHash *externalapi.DomainHash) error
	LastUTXODebugReproducedRootHash(dbContext DBReader) (*externalapi.DomainHash, error)

	// SetPruningPointUTXOSetVerification/PruningPointUTXOSetVerification persist the result of
	// comparing the SERVED pruning-point UTXO bucket against the pruning point's own header UTXO
	// commitment. Observation only: nothing reads this marker to decide what to serve or import.
	// Its purpose is that a boot which skips the expensive re-check still has real numbers to
	// report. Not consensus-critical state - written directly, no staging area.
	SetPruningPointUTXOSetVerification(dbContext DBWriter, verification *PruningPointUTXOSetVerification) error
	PruningPointUTXOSetVerification(dbContext DBReader) (*PruningPointUTXOSetVerification, error)

	CacheLen() int
	UnstageAll(stagingArea *StagingArea)
}
