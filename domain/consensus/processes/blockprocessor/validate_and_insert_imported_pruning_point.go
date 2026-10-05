package blockprocessor

import (
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/ruleerrors"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/hardforks"
	"github.com/pkg/errors"
)

func (bp *blockProcessor) validateAndInsertImportedPruningPoint(
	stagingArea *model.StagingArea, newPruningPointHash *externalapi.DomainHash,
) error {
	log.Info("Checking that the given pruning point is the expected pruning point")

	err := bp.validateImportedPruningPointChain(stagingArea, newPruningPointHash)
	if err != nil {
		return err
	}

	log.Infof("Updating consensus state manager according to the new pruning point %s", newPruningPointHash)
	err = bp.consensusStateManager.ImportPruningPointUTXOSet(stagingArea, newPruningPointHash)
	if err != nil {
		return err
	}

	err = bp.updateVirtualAcceptanceDataAfterImportingPruningPoint(stagingArea)
	if err != nil {
		return err
	}

	return nil
}

// validateImportedPruningPointChain runs HTN-006's two checks, gated at
// hardforks.ValidateIBDPruningListVersion.
//
// Both were commented out, under the note "Currently HTN pruning points are messed up, so need to
// disable this check". That is still true: HTN-006 measured a 62.5% blue-score mismatch rate, so
// turning these on for existing block versions would very likely reject the majority of the chain
// that exists. They are restored here as real code behind an unscheduled gate rather than left as
// comments, so that activating them later is a version bump and not an archaeology exercise.
//
// Until the gate is scheduled this returns nil without reading anything, so IBD behaves exactly as
// it does today.
//
// What they check, and why it matters:
//
//   - IsValidPruningPoint: that the pruning point the peer gave us is the one our own headers imply.
//     Without it, IBD adopts whatever pruning point the peer names.
//   - ArePruningPointsInValidChain: that the pruning point list forms a chain back to genesis.
//     Without it, a peer can supply a list with an unrelated or fabricated ancestry.
func (bp *blockProcessor) validateImportedPruningPointChain(
	stagingArea *model.StagingArea, newPruningPointHash *externalapi.DomainHash,
) error {
	active, err := bp.pruningListValidationIsActive(stagingArea, newPruningPointHash)
	if err != nil {
		return err
	}
	if !active {
		return nil
	}

	isValidPruningPoint, err := bp.pruningManager.IsValidPruningPoint(stagingArea, newPruningPointHash)
	if err != nil {
		return err
	}
	if !isValidPruningPoint {
		return errors.Wrapf(ruleerrors.ErrUnexpectedPruningPoint,
			"%s is not a valid pruning point", newPruningPointHash)
	}

	arePruningPointsInValidChain, err := bp.pruningManager.ArePruningPointsInValidChain(stagingArea)
	if err != nil {
		return err
	}
	if !arePruningPointsInValidChain {
		return errors.Wrapf(ruleerrors.ErrInvalidPruningPointsChain,
			"pruning points do not compose a valid chain to genesis")
	}

	return nil
}

// pruningListValidationIsActive reports whether the imported pruning point is at or past
// hardforks.ValidateIBDPruningListVersion.
//
// The version is derived from the pruning point header's own DAA score. That is a weaker anchor
// than the selected-parent derivation used elsewhere - at import time this node has no resolved DAG
// to check it against, which is the very gap HTN-006 is about - but it is the only score available
// at this point, and it is what decides which rules the imported point is judged under rather than
// anything it is judged against. A peer cannot use it to escape the check for long: claiming a low
// DAA score to stay below the activation version produces a pruning point that then fails to line
// up with the headers this node has.
func (bp *blockProcessor) pruningListValidationIsActive(
	stagingArea *model.StagingArea, newPruningPointHash *externalapi.DomainHash,
) (bool, error) {
	if !hardforks.IsScheduled(hardforks.ValidateIBDPruningListVersion) {
		return false, nil
	}
	if len(bp.powScores) == 0 {
		return false, nil
	}

	header, err := bp.blockHeaderStore.BlockHeader(bp.databaseContext, stagingArea, newPruningPointHash)
	if err != nil {
		return false, err
	}
	blockVersion := constants.BlockVersionForDAAScore(bp.powScores, header.DAAScore())
	return hardforks.Active(hardforks.ValidateIBDPruningListVersion, blockVersion), nil
}
