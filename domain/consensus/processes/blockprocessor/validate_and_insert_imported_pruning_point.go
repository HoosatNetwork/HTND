package blockprocessor

import (
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/ruleerrors"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
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

// validateImportedPruningPointChain runs HTN-006's two checks, each behind its own gate:
// IsValidPruningPoint at HardForkGates.ValidateIBDPruningPointVersion and
// ArePruningPointsInValidChain at HardForkGates.ValidateIBDPruningListVersion.
//
// Both were commented out, under the note "Currently HTN pruning points are messed up, so need to
// disable this check". That is still true: HTN-006 measured a 62.5% blue-score mismatch rate, so
// turning these on for existing block versions would very likely reject the majority of the chain
// that exists. They are restored here as real code behind gates rather than left as comments, so
// that activating them later is a version bump and not an archaeology exercise.
//
// Until a network reaches a check's gate, that check does not run, so IBD behaves exactly as it
// does today. The gates are separate because the checks carry different risk: IsValidPruningPoint
// looks only at the imported pruning point and the headers above it, while the list check depends on
// header pruning point commitments, which are trustworthy only from HeaderPruningPointVersion on.
//
// What they check, and why it matters:
//
//   - IsValidPruningPoint: that the pruning point the peer gave us is the one our own headers imply.
//     Without it, IBD adopts whatever pruning point the peer names.
//   - ArePruningPointsInValidChain: that the newest end of the pruning point list matches what the
//     headers commit to: the headers above the pruning point commit to it, and the pruning point
//     its own header commits to is stored just below it. Only that one previous pruning point is
//     checked, because older entries in real lists do not line up with their headers. The second
//     half is skipped when the pruning point was mined before header pruning points were enforced
//     (pruningListAnchorVersion). Without it, a peer can supply a list for an unrelated chain.
func (bp *blockProcessor) validateImportedPruningPointChain(
	stagingArea *model.StagingArea, newPruningPointHash *externalapi.DomainHash,
) error {
	blockVersion, ok, err := bp.importedPruningPointVersion(stagingArea, newPruningPointHash)
	if err != nil || !ok {
		return err
	}

	if dagconfig.HardForkActive(bp.hardForkGates.ValidateIBDPruningPointVersion, blockVersion) {
		isValidPruningPoint, err := bp.pruningManager.IsValidPruningPoint(stagingArea, newPruningPointHash)
		if err != nil {
			return err
		}
		if !isValidPruningPoint {
			return errors.Wrapf(ruleerrors.ErrUnexpectedPruningPoint,
				"%s is not a valid pruning point", newPruningPointHash)
		}
	}

	if dagconfig.HardForkActive(bp.hardForkGates.ValidateIBDPruningListVersion, blockVersion) {
		arePruningPointsInValidChain, err := bp.pruningManager.ArePruningPointsInValidChain(stagingArea,
			bp.pruningListAnchorVersion())
		if err != nil {
			return err
		}
		if !arePruningPointsInValidChain {
			return errors.Wrapf(ruleerrors.ErrInvalidPruningPointsChain,
				"the pruning point list does not match the pruning point headers' commitments")
		}
	}

	return nil
}

// importedPruningPointVersion returns the block version the imported pruning point's checks are
// gated on. ok is false without an activation table, where no gate can be reached.
//
// The version is derived from the pruning point header's own DAA score. That is a weaker anchor
// than the selected-parent derivation used elsewhere - at import time this node has no resolved DAG
// to check it against, which is the very gap HTN-006 is about - but it is the only score available
// at this point, and it is what decides which rules the imported point is judged under rather than
// anything it is judged against. A peer cannot use it to escape the check for long: claiming a low
// DAA score to stay below the activation version produces a pruning point that then fails to line
// up with the headers this node has.
func (bp *blockProcessor) importedPruningPointVersion(
	stagingArea *model.StagingArea, newPruningPointHash *externalapi.DomainHash,
) (blockVersion uint16, ok bool, err error) {
	if len(bp.powScores) == 0 {
		return 0, false, nil
	}

	header, err := bp.blockHeaderStore.BlockHeader(bp.databaseContext, stagingArea, newPruningPointHash)
	if err != nil {
		return 0, false, err
	}
	return constants.BlockVersionForDAAScore(bp.powScores, header.DAAScore()), true, nil
}
