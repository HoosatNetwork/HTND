package blockvalidator

import (
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/ruleerrors"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/blockversion"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/logger"
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

	// Off for every block version before HardForkGates.MergeSetSizeLimitVersion: it had been
	// disabled with no gate long enough that the existing chain may violate it.
	if !isBlockWithTrustedData {
		active, err := v.hardForkActiveFor(stagingArea, blockHash, v.hardForkGates.MergeSetSizeLimitVersion)
		if err != nil {
			return err
		}
		if active {
			err = v.checkMergeSizeLimit(stagingArea, ghostdagData)
			if err != nil {
				return err
			}
		}
	}

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

	// Off before HardForkGates.IndirectParentsVersion. It was disabled over a performance concern
	// ("think if there is a better way than the whole reachability") that was never measured, so
	// the version it activates at is also where its cost first shows up.
	if !isBlockWithTrustedData {
		active, err := v.hardForkActiveFor(stagingArea, blockHash, v.hardForkGates.IndirectParentsVersion)
		if err != nil {
			return err
		}
		if active {
			err = v.checkIndirectParents(stagingArea, blockHash, header)
			if err != nil {
				return err
			}
		}
	}

	err = v.mergeDepthManager.CheckBoundedMergeDepth(stagingArea, blockHash, ghostdagData, header, isBlockWithTrustedData)
	if err != nil {
		return err
	}

	// Check that none of the parents are disqualified or invalid
	err = v.checkParentsStatus(stagingArea, header)
	if err != nil {
		return err
	}

	// The four header-field checks below are each off before their own gate. Before it, a header's
	// DAA score, blue work, blue score and pruning point are adopted as the peer claims them
	// (HTN-006, HTN-001), and HTN-006 measured a 62.5% blue-score mismatch on the existing mainnet
	// chain, so enabling any of them for existing versions would reject history. Blocks with trusted
	// data are exempt: their GHOSTDAG and DAA data came with them rather than being computed here.
	if !isBlockWithTrustedData {
		err = v.checkGatedHeaderFields(stagingArea, blockHash, header, ghostdagData)
		if err != nil {
			return err
		}
	}

	return nil
}

// checkGatedHeaderFields runs each of the header-field checks whose HardForkGates version blockHash
// has reached.
func (v *blockValidator) checkGatedHeaderFields(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash,
	header externalapi.BlockHeader, ghostdagData *externalapi.BlockGHOSTDAGData,
) error {
	checks := []struct {
		activationVersion uint16
		check             func() error
	}{
		{v.hardForkGates.HeaderDAAScoreVersion, func() error { return v.checkDAAScore(stagingArea, blockHash, header) }},
		{v.hardForkGates.HeaderBlueWorkVersion, func() error { return v.checkBlueWork(ghostdagData, header) }},
		{v.hardForkGates.HeaderBlueScoreVersion, func() error { return v.checkHeaderBlueScore(ghostdagData, header) }},
		{v.hardForkGates.HeaderPruningPointVersion, func() error { return v.validateHeaderPruningPoint(stagingArea, blockHash, header) }},
	}
	for _, c := range checks {
		active, err := v.hardForkActiveFor(stagingArea, blockHash, c.activationVersion)
		if err != nil {
			return err
		}
		if !active {
			continue
		}
		err = c.check()
		if err != nil {
			return err
		}
	}
	return nil
}

// hardForkActiveFor reports whether the rule gated at activationVersion applies to blockHash. The
// version is derived from blockHash's selected parent's DAA score as this node computed it, never from
// the header's version field, for the reason given on checkHeaderBits. A gate no network schedules is
// answered without reading the DAG.
func (v *blockValidator) hardForkActiveFor(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash,
	activationVersion uint16,
) (bool, error) {
	if activationVersion == ^uint16(0) || blockHash.Equal(v.genesisHash) {
		return false, nil
	}
	blockVersion, err := blockversion.OfSelectedParent(v.databaseContext, stagingArea,
		v.ghostdagDataStores[0], v.daaBlocksStore, v.POWScores, blockHash)
	if err != nil {
		return false, err
	}
	return dagconfig.HardForkActive(activationVersion, blockVersion), nil
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

// checkIndirectParents checks that the header's parents at every level are the ones this node builds
// from its direct parents. The DAA score both inputs are keyed on is the one this node computed for
// the block, the same one the block builder passes when it builds a template, never the header's.
func (v *blockValidator) checkIndirectParents(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash,
	header externalapi.BlockHeader,
) error {
	daaScore, err := v.daaBlocksStore.DAAScore(v.databaseContext, stagingArea, blockHash)
	if err != nil {
		return err
	}
	newBlockParents := constants.BlockVersionForDAAScore(v.POWScores, daaScore) >= 7
	expectedParents, err := v.blockParentBuilder.BuildParents(stagingArea, daaScore, header.DirectParents(), newBlockParents)
	if err != nil {
		return err
	}

	areParentsEqual := externalapi.ParentsEqual(header.Parents(), expectedParents)
	if !areParentsEqual {
		return errors.Wrapf(ruleerrors.ErrUnexpectedParents, "unexpected indirect block parents")
	}
	return nil
}

// checkDAAScore checks that the header's DAA score is the one this node computed from the block's
// DAA window. It is exact: the slack of 10 the previously disabled version allowed was one-sided and
// let a header claim any score above the real one.
func (v *blockValidator) checkDAAScore(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash,
	header externalapi.BlockHeader,
) error {
	expectedDAAScore, err := v.daaBlocksStore.DAAScore(v.databaseContext, stagingArea, blockHash)
	if err != nil {
		return err
	}
	if header.DAAScore() != expectedDAAScore {
		return errors.Wrapf(ruleerrors.ErrUnexpectedDAAScore, "block DAA score of %d is not the expected value of %d",
			header.DAAScore(), expectedDAAScore)
	}
	return nil
}

// checkBlueWork checks that the header's blue work is the blue work GHOSTDAG computed for the block.
func (v *blockValidator) checkBlueWork(ghostdagData *externalapi.BlockGHOSTDAGData, header externalapi.BlockHeader) error {
	if header.BlueWork().Cmp(ghostdagData.BlueWork()) != 0 {
		return errors.Wrapf(ruleerrors.ErrUnexpectedBlueWork,
			"block blue work of %d is not the expected value of %d", header.BlueWork(), ghostdagData.BlueWork())
	}
	return nil
}

// checkHeaderBlueScore checks that the header's blue score is the blue score GHOSTDAG computed for
// the block.
func (v *blockValidator) checkHeaderBlueScore(ghostdagData *externalapi.BlockGHOSTDAGData, header externalapi.BlockHeader) error {
	if header.BlueScore() != ghostdagData.BlueScore() {
		return errors.Wrapf(ruleerrors.ErrUnexpectedBlueScore,
			"block blue score of %d is not the expected value of %d", header.BlueScore(), ghostdagData.BlueScore())
	}
	return nil
}
