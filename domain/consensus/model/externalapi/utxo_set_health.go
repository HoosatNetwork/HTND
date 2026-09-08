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
	// commitment in the pruning point's own header. False means either that they disagree, or that
	// the node could not check - both of which are reasons not to trust this node's UTXO set, so
	// they deliberately share an answer. Callers that need to tell them apart have Checked.
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
