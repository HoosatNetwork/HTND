package externalapi

// UTXOSetHealth answers one question: does this node's UTXO baseline hash to the commitment it is
// supposed to hash to?
//
// It exists because "is this node synced" and "is this node's data correct" are different
// questions, and until now only the first had an answer. A node whose imported pruning-point UTXO
// set is missing coins still syncs, still serves, and still reports isSynced - while rejecting
// transactions the network accepted and reporting balances that disagree with other nodes. Nothing
// distinguished it from a healthy node, so an exchange, wallet or explorer pointed at one could not
// tell.
type UTXOSetHealth struct {
	// BaselineVerified is true only when the pruning point's stored multiset matches the UTXO
	// commitment in the pruning point's own header, AND no descendant of the pruning point has since
	// demonstrated that the set is offset from the network's despite that match (see
	// consensusstatemanager.confirmBaselineOffsetIfBoundaryBlock - a set that hashes correctly at the
	// pruning point can still be wrong one block later). False means the hashes disagree, a
	// descendant proved they should not be trusted anyway, or the node could not check - all reasons
	// not to trust this node's UTXO set, so they deliberately share an answer. Callers that need to
	// tell them apart have Checked.
	BaselineVerified bool

	// Checked distinguishes "verified false because they disagree" from "verified false because
	// there was nothing to compare yet" - a node still on genesis, or one whose pruning point or
	// multiset is not readable.
	Checked bool

	// PruningPoint, StoredMultiset and HeaderCommitment are the three values the answer was derived
	// from, so a disagreement can be diagnosed without re-deriving it. Nil when Checked is false.
	PruningPoint     *DomainHash
	StoredMultiset   *DomainHash
	HeaderCommitment *DomainHash
}

// ServedUTXOSetHealth answers the question a node must answer before it hands its pruning point
// UTXO set to a peer: does the set it would actually send hash to the commitment in that pruning
// point's header?
//
// UTXOSetHealth cannot answer this. It compares the pruning point's per-block multiset, not the
// served bucket, and it also reports false for reasons that say nothing about the served set: a
// pruning point that is still genesis or has no readable multiset (Checked=false), and a descendant
// of the pruning point that failed its own commitment check (HTN-208). Gating serving on it made
// nodes whose bucket was fine refuse every peer. The receiving peer verifies exactly the property
// this reports, so this is the check that predicts whether serving is useful.
type ServedUTXOSetHealth struct {
	// Verified is true only when the multiset of every entry in the served pruning point UTXO bucket
	// equals HeaderCommitment. It is false when they differ, and also when Ready is false.
	Verified bool

	// Ready is false when no answer could be given now: the pruning point UTXO set is being
	// rewritten, or the pruning point moved while the set was being hashed. Retrying later can
	// succeed; neither case says the set is bad.
	Ready bool

	// PruningPoint, SetMultiset, HeaderCommitment and EntryCount are the values the answer was
	// derived from. SetMultiset and EntryCount are unset when Ready is false.
	PruningPoint     *DomainHash
	SetMultiset      *DomainHash
	HeaderCommitment *DomainHash
	EntryCount       uint64
}
