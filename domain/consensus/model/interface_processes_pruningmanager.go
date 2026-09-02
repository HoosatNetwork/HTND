package model

import "github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"

// PruningManager resolves and manages the current pruning point
type PruningManager interface {
	UpdatePruningPointByVirtual(stagingArea *StagingArea) error
	IsValidPruningPoint(stagingArea *StagingArea, blockHash *externalapi.DomainHash) (bool, error)
	ArePruningPointsViolatingFinality(stagingArea *StagingArea, pruningPoints []externalapi.BlockHeader) (bool, error)
	ArePruningPointsInValidChain(stagingArea *StagingArea) (bool, error)
	ClearImportedPruningPointData() error
	AppendImportedPruningPointUTXOs(outpointAndUTXOEntryPairs []*externalapi.OutpointAndUTXOEntryPair) error
	UpdatePruningPointIfRequired() error
	PruneAllBlocksBelow(stagingArea *StagingArea, pruningPointHash *externalapi.DomainHash) error
	PruningPointAndItsAnticone() ([]*externalapi.DomainHash, error)
	ExpectedHeaderPruningPoint(stagingArea *StagingArea, blockHash *externalapi.DomainHash) (*externalapi.DomainHash, error)
	TrustedBlockAssociatedGHOSTDAGDataBlockHashes(stagingArea *StagingArea, blockHash *externalapi.DomainHash) ([]*externalapi.DomainHash, error)
	VerifyCurrentPruningPointUTXOSet()
	FindAndReproduceRootDisqualification(stagingArea *StagingArea)

	// RecordPruningPointUTXOSetVerification hashes the served pruning-point UTXO bucket, compares
	// it to the pruning point's header UTXO commitment, and persists the verdict as a marker. It
	// never changes what is built, stored or served - on a mismatch it logs and records
	// "unverified" and returns normally. The bucket scan is O(UTXO set), so callers on a hot path
	// should not block on it.
	RecordPruningPointUTXOSetVerification(stagingArea *StagingArea) (*PruningPointUTXOSetVerification, error)

	// LogPruningPointUTXOSetStatus emits the one-line header/bucket/per-block/diff-chain summary
	// for the current pruning point. Cheap by design: it reports the persisted marker rather than
	// re-hashing the bucket, and kicks the expensive comparison off in the background when the
	// marker is missing or belongs to an older pruning point.
	LogPruningPointUTXOSetStatus()
}
