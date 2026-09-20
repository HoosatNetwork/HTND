package blockvalidator

import (
	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/ruleerrors"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/infrastructure/db/database"
	"github.com/HoosatNetwork/HTND/infrastructure/logger"
	"github.com/pkg/errors"
)

// ValidateHeaderInContext validates block headers in the context of the current
// consensus state
func (v *blockValidator) ValidateHeaderInContext(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash, isBlockWithTrustedData bool) error {
	onEnd := logger.LogAndMeasureExecutionTime(log, "ValidateHeaderInContext")
	defer onEnd()

	header, err := v.blockHeaderStore.BlockHeader(v.databaseContext, stagingArea, blockHash)
	if err != nil {
		return err
	}

	hasValidatedHeader, err := v.hasValidatedHeader(stagingArea, blockHash)
	if err != nil {
		return err
	}

	ghostdagData, err := v.ghostdagDataStores[0].Get(v.databaseContext, stagingArea, blockHash, false)
	if err != nil {
		return err
	}

	if !hasValidatedHeader {
		var logErr error
		log.Debugf("block %s blue score is %d", blockHash, ghostdagData.BlueScore())
		if logErr != nil {
			return logErr
		}
	}

	err = v.validateMedianTime(stagingArea, header)
	if err != nil {
		return err
	}

	// DISABLED, not gated: no ticket, no recorded reason, and no activation version. It has been off
	// long enough that the live chain may well contain blocks that violate it, so it cannot simply be
	// switched on - it needs the same treatment as the four rules in the hardforks package: measure
	// how much of the existing chain would fail, then gate it. Left as-is here rather than deleted so
	// the check itself is not lost.
	// err = v.checkMergeSizeLimit(stagingArea, ghostdagData)
	// if err != nil {
	// 	return err
	// }

	// If needed - calculate reachability data right before calling CheckBoundedMergeDepth,
	// since it's used to find a block's finality point.
	// This might not be required if this block's header has previously been received during
	// headers-first synchronization.
	hasReachabilityData, err := v.reachabilityStore.HasReachabilityData(v.databaseContext, stagingArea, blockHash)
	if err != nil {
		return err
	}
	if !hasReachabilityData {
		err = v.reachabilityManager.AddBlock(stagingArea, blockHash)
		if err != nil {
			return err
		}
	}

	// DISABLED, not gated. The original note is a performance concern ("think if there is a better
	// way than the whole reachability"), not a correctness one, so unlike the checks below this may
	// be a cost question rather than a compatibility question - but it has never been measured, and
	// an unmeasured disabled consensus check is indistinguishable from a compatibility one.
	// TODO: Think if there is better way to check for indirect parents than the whole reachability.
	// if !isBlockWithTrustedData {
	// 	err = v.checkIndirectParents(stagingArea, header)
	// 	if err != nil {
	// 		return err
	// 	}
	// }

	err = v.mergeDepthManager.CheckBoundedMergeDepth(stagingArea, blockHash, ghostdagData, header, isBlockWithTrustedData)
	if err != nil {
		return err
	}

	// Check that none of the parents are disqualified or invalid
	err = v.checkParentsStatus(stagingArea, header)
	if err != nil {
		return err
	}

	// The four checks below are DISABLED and NOT gated. Each is a real consensus check that this
	// node does not perform, so each is a way two nodes can disagree, and none of them is safe to
	// simply re-enable.
	//
	// The "enable these on block v6" note is stale: version 6 activated long ago (mainnet POWScores)
	// and these are still off, so nothing about reaching v6 resolved the underlying problem. They
	// are left here, labelled, rather than deleted - deleting them would lose the checks, and
	// enabling them would reject history.
	//
	// The next step for any of them is the one the hardforks package exists for: measure how much of
	// the existing chain fails the check, then gate it at a new block version. See HTN-006 for the
	// blue-score/blue-work half, which measured a 62.5% mismatch rate - i.e. re-enabling those two
	// today would reject the majority of the chain.
	//
	// if !isBlockWithTrustedData {

	// DISABLED, not gated. Relates to HTN-006: a header's claimed DAA score is adopted unchecked.
	// err = v.checkDAAScore(stagingArea, blockHash, header)
	// if err != nil {
	// 	return err
	// }

	// DISABLED, not gated. HTN-006: IBD adopts the peer's header-claimed blue work without
	// validation.
	// err = v.checkBlueWork(stagingArea, ghostdagData, header)
	// if err != nil {
	// 	return err
	// }

	// DISABLED, not gated. HTN-006: same for blue score; this is where the measured 62.5% mismatch
	// rate would bite.
	// err = v.checkHeaderBlueScore(stagingArea, ghostdagData, header)
	// if err != nil {
	// 	return err
	// }

	// DISABLED, not gated. HTN-001 cites this exact line: nothing cross-checks a node's chosen
	// pruning point, which is half of why two nodes with identical blocks could pick different ones.
	// The original note reads "probably can never again be enabled" - if that is true it should be
	// deleted with a recorded decision rather than left looking like a TODO. The import-time half of
	// this question is gated at hardforks.ValidateIBDPruningListVersion.
	// err = v.validateHeaderPruningPoint(stagingArea, blockHash)
	// if err != nil {
	// 	return err
	// }
	// }

	return nil
}

func (v *blockValidator) hasValidatedHeader(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash) (bool, error) {
	exists, err := v.blockStatusStore.Exists(v.databaseContext, stagingArea, blockHash)
	if err != nil {
		return false, err
	}

	if !exists {
		return false, nil
	}

	status, err := v.blockStatusStore.Get(v.databaseContext, stagingArea, blockHash)
	if database.IsNotFoundError(err) {
		log.Infof("hasValidatedHeader in context failed to retrieve with %s\n", blockHash)
		return false, err
	}
	if err != nil {
		return false, err
	}

	return status == externalapi.StatusHeaderOnly, nil
}

// checkParentsStatus validates that none of the block's parents are disqualified, invalid, or pending
func (v *blockValidator) checkParentsStatus(stagingArea *model.StagingArea, header externalapi.BlockHeader) error {
	directParents := header.DirectParents()
	if len(directParents) == 0 {
		// Genesis block has no parents
		return nil
	}

	for _, parentHash := range directParents {
		status, err := v.blockStatusStore.Get(v.databaseContext, stagingArea, parentHash)
		if database.IsNotFoundError(err) {
			// log.Infof("checkParentsStatus failed to retrieve with %s\n", parentHash)
			continue
		}
		if err != nil {
			return err
		}

		// Reject blocks with parents that are disqualified or invalid
		if status == externalapi.StatusInvalid {
			return errors.Wrapf(ruleerrors.ErrInvalidBlockParent, "block has parent %s with invalid status %s",
				parentHash, status)
		}
	}

	return nil
}

// checkParentsIncest validates that no parent is an ancestor of another parent
func (v *blockValidator) checkParentsIncest(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash) error {
	parents, err := v.dagTopologyManagers[0].Parents(stagingArea, blockHash)
	if err != nil {
		return err
	}

	for _, parentA := range parents {
		for _, parentB := range parents {
			if parentA.Equal(parentB) {
				continue
			}

			isAAncestorOfB, err := v.dagTopologyManagers[0].IsAncestorOf(stagingArea, parentA, parentB)
			if err != nil {
				return err
			}

			if isAAncestorOfB {
				return errors.Wrapf(ruleerrors.ErrInvalidParentsRelation, "parent %s is an "+
					"ancestor of another parent %s",
					parentA,
					parentB,
				)
			}
		}
	}
	return nil
}

func (v *blockValidator) validateMedianTime(stagingArea *model.StagingArea, header externalapi.BlockHeader) error {
	if len(header.DirectParents()) == 0 {
		return nil
	}

	// Ensure the timestamp for the block header is not before the
	// median time of the last several blocks (medianTimeBlocks).
	hash := consensushashing.HeaderHash(header)
	pastMedianTime, err := v.pastMedianTimeManager.PastMedianTime(stagingArea, hash)
	if err != nil {
		return err
	}

	// Allow a small tolerance for clock drift, especially during IBD
	// Blocks must have timestamp >= pastMedianTime - tolerance
	if header.TimeInMilliseconds() < pastMedianTime-int64(v.pastMedianTimeValidationTolerance) {
		return errors.Wrapf(ruleerrors.ErrTimeTooOld, "block timestamp of %d is not after expected %d",
			header.TimeInMilliseconds(), pastMedianTime)
	}

	return nil
}

func (v *blockValidator) checkMergeSizeLimit(_ *model.StagingArea, ghostdagData *externalapi.BlockGHOSTDAGData) error {
	mergeSetSize := len(ghostdagData.MergeSetBlues()) + len(ghostdagData.MergeSetReds())
	mergeSetSizeInt64 := int64(mergeSetSize)
	if mergeSetSizeInt64 < 0 {
		return errors.Wrapf(ruleerrors.ErrViolatingMergeLimit,
			"block merge set size %d cannot be negative", mergeSetSize)
	}
	mergeSetSizeUint64 := uint64(mergeSetSizeInt64)

	if mergeSetSizeUint64 > v.mergeSetSizeLimit {
		return errors.Wrapf(ruleerrors.ErrViolatingMergeLimit,
			"The block merges %d blocks > %d merge set size limit", mergeSetSize, v.mergeSetSizeLimit)
	}

	return nil
}

func (v *blockValidator) blockVersionForDAAScore(daaScore uint64) uint16 {
	var blockVersion uint16 = 1
	for _, powScore := range v.POWScores {
		if daaScore >= powScore {
			blockVersion++
		}
	}
	return blockVersion
}

func (v *blockValidator) checkIndirectParents(stagingArea *model.StagingArea, header externalapi.BlockHeader) error {
	newBlockParents := false
	if v.blockVersionForDAAScore(header.DAAScore()) >= 7 {
		newBlockParents = true
	}
	expectedParents, err := v.blockParentBuilder.BuildParents(stagingArea, header.DAAScore(), header.DirectParents(), newBlockParents)
	if err != nil {
		return err
	}

	areParentsEqual := externalapi.ParentsEqual(header.Parents(), expectedParents)
	if !areParentsEqual {
		return errors.Wrapf(ruleerrors.ErrUnexpectedParents, "unexpected indirect block parents")
	}
	return nil
}

//lint:ignore U1000 check is intentionally disabled for now (see ValidateHeaderInContext).
func (v *blockValidator) checkDAAScore(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash,
	header externalapi.BlockHeader,
) error {
	expectedDAAScore, err := v.daaBlocksStore.DAAScore(v.databaseContext, stagingArea, blockHash)
	if err != nil {
		return err
	}
	var threshold uint64 = 10
	if header.DAAScore()+threshold < expectedDAAScore {
		return errors.Wrapf(ruleerrors.ErrUnexpectedDAAScore, "block DAA score of %d is not the expected value of %d", header.DAAScore(), expectedDAAScore)
	}
	return nil
}

// func (v *blockValidator) checkBlueWork(_ *model.StagingArea, ghostdagData *externalapi.BlockGHOSTDAGData,
// 	header externalapi.BlockHeader,
// ) error {
// 	expectedBlueWork := ghostdagData.BlueWork()
// 	headerBlueWork := header.BlueWork()

// 	if headerBlueWork.Cmp(expectedBlueWork) > 0 {
// 		return errors.Wrapf(ruleerrors.ErrUnexpectedBlueWork,
// 			"block blue work %d is ahead of the expected blue work of %d",
// 			headerBlueWork, expectedBlueWork)
// 	}
// 	return nil
// }

// func (v *blockValidator) checkHeaderBlueScore(_ *model.StagingArea, ghostdagData *externalapi.BlockGHOSTDAGData,
// 	header externalapi.BlockHeader,
// ) error {
// 	expectedBlueScore := ghostdagData.BlueScore()
// 	headerBlueScore := header.BlueScore()

// 	if headerBlueScore > expectedBlueScore {
// 		return errors.Wrapf(ruleerrors.ErrUnexpectedBlueScore,
// 			"block blue score of %d is ahead of the expected blue score of %d",
// 			headerBlueScore, expectedBlueScore)
// 	}
// 	return nil
// }
