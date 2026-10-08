package blockprocessor

// pruningListAnchorVersion is the block version from which a pruning point's header commitment is a consensus rule:
// HardForkGates.HeaderPruningPointVersion. ArePruningPointsInValidChain follows the current pruning point's header
// commitment to the previous pruning point only when the current pruning point is at or above it.
//
// It has a file of its own because the way a gate is read differs between release branches, and the code that calls
// it does not.
func (bp *blockProcessor) pruningListAnchorVersion() uint16 {
	return bp.hardForkGates.HeaderPruningPointVersion
}
