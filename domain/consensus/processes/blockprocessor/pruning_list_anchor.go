package blockprocessor

// pruningListAnchorVersion is the block version from which a
// pruning point's header commitment is a consensus rule. This
// branch has no header pruning point gate, so no block version
// reaches it, and ArePruningPointsInValidChain checks only the
// current pruning point against the headers above it.
func (bp *blockProcessor) pruningListAnchorVersion() uint16 {
	return ^uint16(0)
}
