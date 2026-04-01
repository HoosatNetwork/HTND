package consensusstatemanager

import (
	"github.com/Hoosat-Oy/HTND/domain/consensus/database"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/infrastructure/logger"
	"github.com/pkg/errors"
)

// ValidateUTXODiffChildChains validates that all blocks have proper UTXO diff child chains
// and repairs them if needed. This should be called at node startup to ensure UTXO set
// integrity for serving to other peers during IBD.
func (csm *consensusStateManager) ValidateUTXODiffChildChains() error {
	onEnd := logger.LogAndMeasureExecutionTime(log, "ValidateUTXODiffChildChains")
	defer onEnd()

	log.Info("Starting UTXO diff child chain validation...")

	stagingArea := model.NewStagingArea()

	// Get the current pruning point
	pruningPoint, err := csm.pruningStore.PruningPoint(csm.databaseContext, stagingArea)
	if err != nil {
		return err
	}

	// Check if we can reach virtual from pruning point via UTXO diff children
	log.Infof("Checking UTXO diff child chain from pruning point %s to virtual", pruningPoint)

	virtualGHOSTDAG, err := csm.ghostdagDataStore.Get(csm.databaseContext, stagingArea, model.VirtualBlockHash, false)
	if err != nil {
		if database.IsNotFoundError(err) {
			log.Info("Virtual block not found, skipping UTXO diff validation")
			return nil
		}
		return err
	}

	virtualSelectedParent := virtualGHOSTDAG.SelectedParent()
	if virtualSelectedParent == nil {
		log.Info("Virtual has no selected parent, skipping UTXO diff validation")
		return nil
	}

	// Try to traverse from virtual selected parent back to pruning point
	currentHash := virtualSelectedParent
	blocksTraversed := 0
	maxTraversal := 100000 // Safety limit

	for blocksTraversed < maxTraversal {
		if currentHash.Equal(pruningPoint) {
			log.Infof("UTXO diff child chain is valid. Traversed %d blocks from virtual to pruning point", blocksTraversed)
			return nil
		}

		// Check if current block has UTXO diff child
		hasChild, err := csm.utxoDiffStore.HasUTXODiffChild(csm.databaseContext, stagingArea, currentHash)
		if err != nil {
			return errors.Wrapf(err, "failed to check UTXO diff child for block %s", currentHash)
		}

		if !hasChild {
			log.Warnf("Block %s is missing UTXO diff child link. Chain is broken at %d blocks from virtual.",
				currentHash, blocksTraversed)
			return csm.repairUTXODiffChildChains(stagingArea, pruningPoint, virtualSelectedParent)
		}

		// Get the UTXO diff child
		childHash, err := csm.utxoDiffStore.UTXODiffChild(csm.databaseContext, stagingArea, currentHash)
		if err != nil {
			return errors.Wrapf(err, "failed to get UTXO diff child for block %s", currentHash)
		}

		// Move to the child (which is actually further back in the chain toward pruning point)
		currentHash = childHash
		blocksTraversed++
	}

	log.Warnf("UTXO diff child chain traversal exceeded safety limit of %d blocks", maxTraversal)
	return csm.repairUTXODiffChildChains(stagingArea, pruningPoint, virtualSelectedParent)
}

// repairUTXODiffChildChains repairs broken UTXO diff child chains by rebuilding them
// using the selected parent chain
func (csm *consensusStateManager) repairUTXODiffChildChains(
	stagingArea *model.StagingArea,
	pruningPoint, tip *externalapi.DomainHash,
) error {
	onEnd := logger.LogAndMeasureExecutionTime(log, "repairUTXODiffChildChains")
	defer onEnd()

	log.Infof("Repairing UTXO diff child chains from pruning point %s to tip %s", pruningPoint, tip)

	// Build the selected parent chain from tip back to pruning point
	selectedChain := []*externalapi.DomainHash{tip}
	currentHash := tip

	for !currentHash.Equal(pruningPoint) {
		ghostdagData, err := csm.ghostdagDataStore.Get(csm.databaseContext, stagingArea, currentHash, false)
		if err != nil {
			return errors.Wrapf(err, "failed to get GHOSTDAG data for block %s", currentHash)
		}

		selectedParent := ghostdagData.SelectedParent()
		if selectedParent == nil {
			return errors.Errorf("block %s has no selected parent but hasn't reached pruning point", currentHash)
		}

		selectedChain = append(selectedChain, selectedParent)
		currentHash = selectedParent

		// Safety check
		if len(selectedChain) > 100000 {
			return errors.Errorf("selected chain from tip to pruning point exceeds 100000 blocks")
		}
	}

	log.Infof("Found selected parent chain of length %d blocks", len(selectedChain))

	// Now rebuild the UTXO diff child links
	// The chain is [tip -> ... -> pruningPoint], so we reverse it to [pruningPoint -> ... -> tip]
	// and set up diff children going forward
	log.Info("Checking and rebuilding UTXO diff child links...")

	repairCount := 0
	for i := len(selectedChain) - 1; i > 0; i-- {
		parentHash := selectedChain[i]
		childHash := selectedChain[i-1]

		// Check if parent already has a UTXO diff child
		hasChild, err := csm.utxoDiffStore.HasUTXODiffChild(csm.databaseContext, stagingArea, parentHash)
		if err != nil {
			return err
		}

		if hasChild {
			existingChild, err := csm.utxoDiffStore.UTXODiffChild(csm.databaseContext, stagingArea, parentHash)
			if err != nil {
				return err
			}

			if existingChild.Equal(childHash) {
				// Link is already correct, skip
				continue
			}

			// Parent has a diff child, but it's not our child
			// This is OK - it means the UTXO diff chain follows a different path
			// DON'T overwrite it, as that would corrupt the existing valid chain
			log.Debugf("Block %s already has UTXO diff child %s (not in selected parent chain %s)",
				parentHash, existingChild, childHash)
			continue
		}

		// Parent has NO diff child - check if we can create one
		childUTXODiff, err := csm.utxoDiffStore.UTXODiff(csm.databaseContext, stagingArea, childHash)
		if err != nil {
			// Child has no UTXO diff, so we can't create a link
			log.Debugf("Block %s has no UTXO diff, skipping link from %s", childHash, parentHash)
			continue
		}

		// Create the missing child link
		log.Debugf("Creating missing UTXO diff child link: %s -> %s", parentHash, childHash)
		csm.utxoDiffStore.Stage(stagingArea, childHash, childUTXODiff, parentHash)
		repairCount++

		// Commit in batches to avoid memory issues
		if repairCount%1000 == 0 {
			log.Infof("Created %d missing UTXO diff child links so far...", repairCount)
			err = csm.commitStagingArea(stagingArea)
			if err != nil {
				return err
			}
			stagingArea = model.NewStagingArea()
		}
	}

	// Final commit
	if repairCount%1000 != 0 {
		err := csm.commitStagingArea(stagingArea)
		if err != nil {
			return err
		}
	}

	log.Infof("Successfully created %d missing UTXO diff child links", repairCount)
	return nil
}

// commitStagingArea is a helper to commit the staging area changes
func (csm *consensusStateManager) commitStagingArea(stagingArea *model.StagingArea) error {
	dbTx, err := csm.databaseContext.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = dbTx.RollbackUnlessClosed() }()

	err = stagingArea.Commit(dbTx)
	if err != nil {
		return err
	}

	return dbTx.Commit()
}
