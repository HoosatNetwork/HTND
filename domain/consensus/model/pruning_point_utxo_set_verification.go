package model

import "github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"

// PruningPointUTXOSetStatus is the verdict of comparing the pruning-point UTXO set this node
// SERVES to syncing peers (pruningStore's pruning-point-utxo-set bucket) against the pruning
// point's own header UTXO commitment.
//
// The two are separate stores maintained by separate code paths: the served bucket is rebuilt by
// pruningManager.updatePruningPoint from a previous-to-current pruning point diff, while the
// header commitment is what the network committed to. Nothing forced them to agree, and until
// this marker existed nothing recorded whether they did.
type PruningPointUTXOSetStatus byte

const (
	// PruningPointUTXOSetUnknown means no comparison has been recorded for the pruning point in
	// question - either none has ever run, or the recorded one belongs to a different pruning point.
	PruningPointUTXOSetUnknown PruningPointUTXOSetStatus = iota

	// PruningPointUTXOSetVerified means the served bucket hashed to the header commitment exactly.
	PruningPointUTXOSetVerified

	// PruningPointUTXOSetUnverified means it did not. The bucket is still built, still stored and
	// still served: this marker is observation only and changes no behaviour.
	PruningPointUTXOSetUnverified
)

// String returns the marker status as it appears in the startup line.
func (status PruningPointUTXOSetStatus) String() string {
	switch status {
	case PruningPointUTXOSetVerified:
		return "verified"
	case PruningPointUTXOSetUnverified:
		return "unverified"
	case PruningPointUTXOSetUnknown:
		return "unknown"
	default:
		return "unknown"
	}
}

// PruningPointUTXOSetVerification is a durable record of one three-way comparison at one pruning
// point, persisted so that a later boot can print real numbers without repeating the full
// bucket scan (which is minutes on a mature chain).
//
// It exists because the expensive comparison is memoised - see
// pruningManager.VerifyCurrentPruningPointUTXOSet, which skips entirely when the pruning point
// hasn't moved since the last check. A cached skip used to leave the operator with no numbers at
// all; storing the hashes here means a skip still has something honest to print.
type PruningPointUTXOSetVerification struct {
	// PruningPoint is the pruning point these hashes describe. A marker whose PruningPoint differs
	// from the current one is stale and must be reported as unknown, never as a current verdict.
	PruningPoint *externalapi.DomainHash

	// HeaderCommitment is blockHeaderStore.BlockHeader(PruningPoint).UTXOCommitment() - the only
	// externally anchored value of the three.
	HeaderCommitment *externalapi.DomainHash

	// BucketMultiset is a fresh MuHash over every entry in the served pruning-point UTXO bucket.
	BucketMultiset *externalapi.DomainHash

	// PerBlockMultiset is the pruning point's stored per-block multiset (calculateMultiset's
	// output). Nil when it could not be read. Note this is a running sum rooted at the import
	// anchor, not an independent reconstruction.
	PerBlockMultiset *externalapi.DomainHash

	// DiffChainMultiset is a MuHash over restorePastUTXO's walk. Nil unless it was computed - it is
	// expensive and has been observed to abort, so it is only attempted under
	// --enable-utxo-debug-diagnostics.
	DiffChainMultiset *externalapi.DomainHash

	// Status is the verdict: BucketMultiset == HeaderCommitment.
	Status PruningPointUTXOSetStatus

	// EntryCount is how many entries were in the bucket when it was hashed.
	EntryCount uint64

	// CheckedAtDAAScore is the virtual DAA score when the comparison ran, so a stale marker can be
	// aged rather than merely detected as belonging to another pruning point.
	CheckedAtDAAScore uint64
}
