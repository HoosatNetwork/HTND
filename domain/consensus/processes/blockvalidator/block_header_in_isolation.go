package blockvalidator

import (
	"github.com/Hoosat-Oy/HTND/domain/consensus/model"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/ruleerrors"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/consensushashing"
	"github.com/Hoosat-Oy/HTND/infrastructure/logger"
	"github.com/Hoosat-Oy/HTND/util/mstime"
	"github.com/pkg/errors"
)

// ValidateHeaderInIsolation validates block headers in isolation from the current
// consensus state
func (v *blockValidator) ValidateHeaderInIsolation(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash) error {
	onEnd := logger.LogAndMeasureExecutionTime(log, "ValidateHeaderInIsolation")
	defer onEnd()

	header, err := v.blockHeaderStore.BlockHeader(v.databaseContext, stagingArea, blockHash)
	if err != nil {
		return err
	}

	if !blockHash.Equal(v.genesisHash) {
		err = v.checkBlockVersion(header)
		if err != nil {
			return err
		}
	}

	err = v.checkBlockTimestampInIsolation(header)
	if err != nil {
		return err
	}

	err = v.checkParentsLimit(header)
	if err != nil {
		return err
	}

	return nil
}

func (v *blockValidator) checkParentsLimit(header externalapi.BlockHeader) error {
	hash := consensushashing.HeaderHash(header)
	if len(header.DirectParents()) == 0 && !hash.Equal(v.genesisHash) {
		return errors.Wrapf(ruleerrors.ErrNoParents, "block has no parents")
	}

	expectedVersion := v.expectedBlockVersion(header.DAAScore())
	if uint64(len(header.DirectParents())) > uint64(v.maxBlockParents[int(expectedVersion)-1]) {
		return errors.Wrapf(ruleerrors.ErrTooManyParents, "block header has %d parents, but the maximum allowed amount "+
			"is %d", len(header.DirectParents()), v.maxBlockParents)
	}
	return nil
}

func (v *blockValidator) expectedBlockVersion(daaScore uint64) uint16 {
	if daaScore == 0 {
		return 1
	}
	version := uint16(1)
	for _, powScore := range v.POWScores {
		if daaScore >= powScore {
			version++
		}
	}
	return version
}

func (v *blockValidator) checkBlockVersion(header externalapi.BlockHeader) error {
	expectedVersion := v.expectedBlockVersion(header.DAAScore())
	// Keep track of the highest version encountered, but validate this header
	// against the version implied by its own DAA score.
	if header.Version() != expectedVersion {
		return errors.Wrapf(
			ruleerrors.ErrWrongBlockVersion, "The block version %d should be %d", header.Version(), expectedVersion)
	}
	return nil
}

func (v *blockValidator) checkBlockTimestampInIsolation(header externalapi.BlockHeader) error {
	blockTimestamp := header.TimeInMilliseconds()
	now := mstime.Now().UnixMilliseconds()
	maxCurrentTime := now + int64(v.timestampDeviationTolerance)*v.targetTimePerBlock[0].Milliseconds()
	if blockTimestamp > maxCurrentTime {
		return errors.Wrapf(
			ruleerrors.ErrTimeTooMuchInTheFuture, "The block timestamp is in the future.")
	}
	return nil
}
