package consensusstatemanager

import (
	"fmt"
	"os"
	"sync/atomic"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/utxo"
)

// verifySelectedParentDiff makes diffFromSelectedParentPast check every diff it computes from a
// block's changes against the full DiffFrom it replaces, and use DiffFrom's answer on any
// difference. On by default while the new computation is being proven on real chains;
// HTND_VERIFY_SELECTED_PARENT_DIFF=0 turns the check off, and with it the cost it exists to remove.
var verifySelectedParentDiff = os.Getenv("HTND_VERIFY_SELECTED_PARENT_DIFF") != "0"

var selectedParentDiffStats struct {
	verified   atomic.Uint64
	mismatches atomic.Uint64
}

// selectedParentDiffVerifiedLogInterval is how many verified diffs pass between progress lines.
const selectedParentDiffVerifiedLogInterval = 10_000

// diffFromSelectedParentPast returns selectedParentPastUTXOSet.DiffFrom(pastUTXOSet), the diff staged
// for a chain block that is not the tip of the resolved chain.
//
// pastUTXOSet is the clone of selectedParentPastUTXOSet that applyMergeSetBlocks changed, and both are
// diffs from virtual, which moves only between resolution chunks. The k-th block of a chunk therefore
// diffed two diffs holding the changes of all k-1 blocks before it, so a chunk cost its length squared
// times its transactions per block, which is why blocks full of transactions resolved so slowly. The
// two differ only at the outpoints this block's transactions touched, and utxo.DiffFromChanged
// computes the same diff over those alone.
func (csm *consensusStateManager) diffFromSelectedParentPast(blockHash *externalapi.DomainHash,
	selectedParentPastUTXOSet, pastUTXOSet externalapi.UTXODiff,
) (externalapi.UTXODiff, error) {
	changedDiff, ok, changedErr := utxo.DiffFromChanged(selectedParentPastUTXOSet, pastUTXOSet)
	if !ok {
		if changedErr != nil {
			log.Warnf("Computing the selected parent diff of block %s from its changes failed (%s); "+
				"diffing the full pasts instead", blockHash, changedErr)
		}
		return selectedParentPastUTXOSet.DiffFrom(pastUTXOSet)
	}
	if !verifySelectedParentDiff {
		return changedDiff, changedErr
	}

	fullDiff, fullErr := selectedParentPastUTXOSet.DiffFrom(pastUTXOSet)
	mismatch := (changedErr == nil) != (fullErr == nil) ||
		(fullErr == nil && !changedDiff.Equal(fullDiff))
	if mismatch {
		mismatches := selectedParentDiffStats.mismatches.Add(1)
		log.Errorf("Selected parent diff of block %s computed from its changes disagrees with the full "+
			"DiffFrom (mismatch #%d) - using the full DiffFrom. Please report this.\n"+
			"from changes: diff %s, error %v\nfull:         diff %s, error %v",
			blockHash, mismatches, changedDiff, changedErr, fullDiff, fullErr)
		// In a test binary, fail loudly: every consensus test that resolves blocks with transactions
		// then checks this computation too.
		if testing.Testing() {
			panic(fmt.Sprintf("selected parent diff of block %s computed from its changes disagrees with "+
				"the full DiffFrom", blockHash))
		}
		return fullDiff, fullErr
	}
	if verified := selectedParentDiffStats.verified.Add(1); verified == 1 ||
		verified%selectedParentDiffVerifiedLogInterval == 0 {
		log.Infof("Selected parent diffs computed from block changes: %d verified against the full "+
			"DiffFrom, %d mismatches", verified, selectedParentDiffStats.mismatches.Load())
	}
	return fullDiff, fullErr
}
