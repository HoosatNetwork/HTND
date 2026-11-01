package consensusstatemanager

import (
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/testapi"
)

// SetReverseUTXODiffsInterruptHook installs a hook that ReverseUTXODiffs calls after each diff it commits, with the
// number committed so far; an error from the hook stops the reversal there, as a crash between two of its commits
// would. It returns a function that restores the previous hook.
func SetReverseUTXODiffsInterruptHook(hook func(committedDiffs int) error) (restore func()) {
	previous := reverseUTXODiffsInterruptHook
	reverseUTXODiffsInterruptHook = hook
	return func() { reverseUTXODiffsInterruptHook = previous }
}

// RestorePastUTXO returns blockHash's past UTXO as restorePastUTXO rebuilds it: a diff relative to virtual's UTXO set,
// composed along the block's UTXO diff children.
func RestorePastUTXO(csm testapi.TestConsensusStateManager, blockHash *externalapi.DomainHash) (externalapi.UTXODiff, error) {
	return csm.(*testConsensusStateManager).restorePastUTXO(model.NewStagingArea(), blockHash)
}
