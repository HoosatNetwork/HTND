package consensusstatemanager

import (
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/database"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/logger"
	"github.com/HoosatNetwork/HTND/v2/util/staging"
)

// reverseUTXODiffsInterruptHook, when set, is called after each diff ReverseUTXODiffs commits, with the number committed
// so far. An error from it stops the reversal there, as a crash between two of its commits would. It is only ever set
// by tests (see export_test.go).
var reverseUTXODiffsInterruptHook func(committedDiffs int) error

func (csm *consensusStateManager) ReverseUTXODiffs(tipHash *externalapi.DomainHash,
	reversalData *model.UTXODiffReversalData,
) error {
	// During the process of resolving a chain of blocks, we temporarily set all blocks' (except the tip)
	// UTXODiffChild to be the selected parent.
	// Once the process is complete, we can reverse said chain, to now go directly to virtual through the relevant tip
	onEnd := logger.LogAndMeasureExecutionTime(log, "reverseUTXODiffs")
	defer onEnd()

	readStagingArea := model.NewStagingArea()

	log.Debugf("Reversing utxoDiffs")

	// Set previousUTXODiff and previousBlock to tip.SelectedParent before we start touching them,
	// since previousBlock's UTXODiff is going to be over-written in the next step
	previousBlock := reversalData.SelectedParentHash
	previousUTXODiff, err := csm.utxoDiffStore.UTXODiff(csm.databaseContext, readStagingArea, previousBlock)
	if err != nil {
		return err
	}

	// tip.selectedParent is special in the sense that we don't have it's diff available in reverse, however,
	// we were able to calculate it when the tip's and tip.selectedParent's UTXOSets were known during resolveBlockStatus.
	// Therefore - we treat it separately
	err = csm.commitUTXODiffInSeparateStagingArea(previousBlock, reversalData.SelectedParentUTXODiff, tipHash)
	if err != nil {
		return err
	}
	committedDiffs := 1
	if reverseUTXODiffsInterruptHook != nil {
		if err := reverseUTXODiffsInterruptHook(committedDiffs); err != nil {
			return err
		}
	}

	log.Trace("Reversed 1 utxoDiff")

	previousBlockGHOSTDAGData, err := csm.ghostdagDataStore.Get(csm.databaseContext, readStagingArea, previousBlock, false)
	if database.IsNotFoundError(err) {
		log.Infof("ReverseUTXODiffs failed to retrieve with %s\n", previousBlock)
		return err
	}
	if err != nil {
		return err
	}
	// Now go over the rest of the blocks and assign for every block Bi.UTXODiff = Bi+1.UTXODiff.Reversed()
	for i := 1; ; i++ {
		currentBlock := previousBlockGHOSTDAGData.SelectedParent()
		log.Debugf("Reversing UTXO diff for %s", currentBlock)

		// Note: A nil/virtual UTXODiffChild is represented by the *absence* of a UTXODiffChild entry.
		// Treat missing UTXODiffChild as a stop condition (rather than an error), since it indicates we reached
		// an existing chain end and should stop reversing beyond it.
		hasChild, err := csm.utxoDiffStore.HasUTXODiffChild(csm.databaseContext, readStagingArea, currentBlock)
		if err != nil {
			return err
		}
		var currentBlockUTXODiffChild *externalapi.DomainHash
		stopAfterCurrent := !hasChild
		if hasChild {
			currentBlockUTXODiffChild, err = csm.utxoDiffStore.UTXODiffChild(csm.databaseContext, readStagingArea, currentBlock)
			if err != nil {
				return err
			}
		}
		currentBlockGHOSTDAGData, err := csm.ghostdagDataStore.Get(csm.databaseContext, readStagingArea, currentBlock, false)
		if err != nil {
			return err
		}

		currentUTXODiff := previousUTXODiff.Reversed()

		// retrieve current utxoDiff for Bi, to be used by next block
		previousUTXODiff, err = csm.utxoDiffStore.UTXODiff(csm.databaseContext, readStagingArea, currentBlock)
		if err != nil {
			return err
		}

		err = csm.commitUTXODiffInSeparateStagingArea(currentBlock, currentUTXODiff, previousBlock)
		if err != nil {
			return err
		}
		committedDiffs++
		if reverseUTXODiffsInterruptHook != nil {
			if err := reverseUTXODiffsInterruptHook(committedDiffs); err != nil {
				return err
			}
		}

		// We stop reversing when current doesn't have a UTXODiffChild (nil/virtual), or when current's UTXODiffChild
		// is not current's SelectedParent.
		if stopAfterCurrent || !currentBlockGHOSTDAGData.SelectedParent().Equal(currentBlockUTXODiffChild) {
			log.Debugf("Finish reversing at %s (hasChild=%t)", currentBlock, hasChild)
			break
		}

		previousBlock = currentBlock
		previousBlockGHOSTDAGData = currentBlockGHOSTDAGData

		log.Tracef("Reversed %d utxoDiffs", i)
	}

	return nil
}

func (csm *consensusStateManager) commitUTXODiffInSeparateStagingArea(
	blockHash *externalapi.DomainHash, utxoDiff externalapi.UTXODiff, utxoDiffChild *externalapi.DomainHash,
) error {
	stagingAreaForCurrentBlock := model.NewStagingArea()

	csm.utxoDiffStore.Stage(stagingAreaForCurrentBlock, blockHash, utxoDiff, utxoDiffChild)

	return staging.CommitAllChanges(csm.databaseContext, stagingAreaForCurrentBlock)
}
