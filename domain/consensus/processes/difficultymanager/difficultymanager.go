package difficultymanager

import (
	"math/big"
	"time"

	"github.com/Hoosat-Oy/HTND/infrastructure/logger"
	"github.com/Hoosat-Oy/HTND/util/math"

	"github.com/Hoosat-Oy/HTND/util/difficulty"

	"github.com/Hoosat-Oy/HTND/domain/consensus/database"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/constants"
)

// DifficultyManager provides a method to resolve the
// difficulty value of a block
type difficultyManager struct {
	databaseContext                model.DBReader
	ghostdagManager                model.GHOSTDAGManager
	ghostdagStore                  model.GHOSTDAGDataStore
	headerStore                    model.BlockHeaderStore
	daaBlocksStore                 model.DAABlocksStore
	dagTopologyManager             model.DAGTopologyManager
	dagTraversalManager            model.DAGTraversalManager
	genesisHash                    *externalapi.DomainHash
	powMax                         *big.Int
	difficultyAdjustmentWindowSize []int
	disableDifficultyAdjustment    bool
	targetTimePerBlock             []time.Duration
	genesisBits                    uint32
}

// New instantiates a new DifficultyManager
func New(databaseContext model.DBReader,
	ghostdagManager model.GHOSTDAGManager,
	ghostdagStore model.GHOSTDAGDataStore,
	headerStore model.BlockHeaderStore,
	daaBlocksStore model.DAABlocksStore,
	dagTopologyManager model.DAGTopologyManager,
	dagTraversalManager model.DAGTraversalManager,

	powMax *big.Int,
	difficultyAdjustmentWindowSize []int,
	disableDifficultyAdjustment bool,
	targetTimePerBlock []time.Duration,
	genesisHash *externalapi.DomainHash,
	genesisBits uint32,
) model.DifficultyManager {
	return &difficultyManager{
		databaseContext:                databaseContext,
		ghostdagManager:                ghostdagManager,
		ghostdagStore:                  ghostdagStore,
		headerStore:                    headerStore,
		daaBlocksStore:                 daaBlocksStore,
		dagTopologyManager:             dagTopologyManager,
		dagTraversalManager:            dagTraversalManager,
		powMax:                         powMax,
		difficultyAdjustmentWindowSize: difficultyAdjustmentWindowSize,
		disableDifficultyAdjustment:    disableDifficultyAdjustment,
		targetTimePerBlock:             targetTimePerBlock,
		genesisHash:                    genesisHash,
		genesisBits:                    genesisBits,
	}
}

// StageDAADataAndReturnRequiredDifficulty calculates the DAA window, stages the DAA score and DAA added
// blocks, and returns the required difficulty for the given block.
// The reason this function both stages DAA data and returns the difficulty is because in order to calculate
// both of them we need to calculate the DAA window, which is a relatively heavy operation, so we reuse the
// block window instead of recalculating it for the two purposes.
// For cases where no staging should happen and the caller only needs to know the difficulty he should
// use RequiredDifficulty.
func (dm *difficultyManager) StageDAADataAndReturnRequiredDifficulty(
	stagingArea *model.StagingArea,
	blockHash *externalapi.DomainHash,
	isBlockWithTrustedData bool,
) (uint32, error) {
	onEnd := logger.LogAndMeasureExecutionTime(log, "StageDAADataAndReturnRequiredDifficulty")
	defer onEnd()

	targetsWindow, err := dm.blockWindow(stagingArea, blockHash, dm.difficultyAdjustmentWindowSize[constants.GetBlockVersion()-1])
	if err != nil {
		return 0, err
	}
	defer targetsWindow.free()

	err = dm.stageDAAScoreAndAddedBlocks(stagingArea, blockHash, targetsWindow.pairs, isBlockWithTrustedData)
	if err != nil {
		return 0, err
	}

	return dm.requiredDifficultyFromTargetsWindow(targetsWindow, blockHash)
}

// RequiredDifficulty returns the difficulty required for some block
func (dm *difficultyManager) RequiredDifficulty(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash) (uint32, error) {
	targetsWindow, err := dm.blockWindow(stagingArea, blockHash, dm.difficultyAdjustmentWindowSize[constants.GetBlockVersion()-1])
	if err != nil {
		return 0, err
	}
	defer targetsWindow.free()

	return dm.requiredDifficultyFromTargetsWindow(targetsWindow, blockHash)
}

func (dm *difficultyManager) requiredDifficultyFromTargetsWindow(targetsWindow blockWindow, blockHash *externalapi.DomainHash) (uint32, error) {
	if dm.disableDifficultyAdjustment {
		return dm.genesisBits, nil
	}

	// in the past this was < 2 as the comment explains, we changed it to under the window size to
	// make the hashrate(which is ~1.5GH/s) constant in the first 2641 blocks so that we won't have a lot of tips

	// We need at least 2 blocks to get a timestamp interval
	// We could instead clamp the timestamp difference to `targetTimePerBlock`,
	// but then everything will cancel out and we'll get the target from the last block, which will be the same as genesis.
	// We add 64 as a safety margin
	if targetsWindow.len() < 2 || targetsWindow.len() < dm.difficultyAdjustmentWindowSize[constants.GetBlockVersion()-1] {
		return dm.genesisBits, nil
	}

	windowMinTimestamp, windowMaxTimeStamp, windowMinIndex := targetsWindow.minMaxTimestamps()
	// Remove the last block from the window so to calculate the average target of dag.difficultyAdjustmentWindowSize blocks
	targetsWindow.remove(windowMinIndex)

	// Calculate new target difficulty as:
	// averageWindowTarget * (windowMinTimestamp / (targetTimePerBlock * windowSize))
	// The result uses integer division which means it will be slightly
	// rounded down.
	div := new(big.Int)
	newTarget := targetsWindow.averageTarget()
	newTarget.
		// We need to clamp the timestamp difference to 1 so that we'll never get a 0 target.
		Mul(newTarget, div.SetInt64(math.MaxInt64(windowMaxTimeStamp-windowMinTimestamp, 1))).
		Div(newTarget, div.SetInt64(dm.targetTimePerBlock[constants.GetBlockVersion()-1].Milliseconds())).
		Div(newTarget, div.SetUint64(uint64(targetsWindow.len())))
	// Check that newTarget is not above maximums possible target.
	if newTarget.Cmp(dm.powMax) > 0 {
		return difficulty.BigToCompact(dm.powMax), nil
	}
	// difficulty bombs
	// if constants.GetBlockVersion() >= 5 {
	// 	stagingArea := model.NewStagingArea()
	// 	daaScore, _ := dm.daaBlocksStore.DAAScore(dm.databaseContext, stagingArea, blockHash)
	// 	if daaScore >= 43334187 && daaScore <= 43335187 {
	// 		newTarget = difficulty.CompactToBig(dm.genesisBits)
	// 	}
	// }
	newTargetBits := difficulty.BigToCompact(newTarget)

	return newTargetBits, nil
}

func (dm *difficultyManager) stageDAAScoreAndAddedBlocks(stagingArea *model.StagingArea,
	blockHash *externalapi.DomainHash,
	windowPairs []*externalapi.BlockGHOSTDAGDataHashPair,
	isBlockWithTrustedData bool,
) error {
	onEnd := logger.LogAndMeasureExecutionTime(log, "stageDAAScoreAndAddedBlocks")
	defer onEnd()

	daaScore, addedBlocks, err := dm.calculateDaaScoreAndAddedBlocks(stagingArea, blockHash, windowPairs, isBlockWithTrustedData)
	if err != nil {
		return err
	}

	dm.daaBlocksStore.StageDAAScore(stagingArea, blockHash, daaScore)
	dm.daaBlocksStore.StageBlockDAAAddedBlocks(stagingArea, blockHash, addedBlocks)
	return nil
}

func (dm *difficultyManager) calculateDaaScoreAndAddedBlocks(stagingArea *model.StagingArea,
	blockHash *externalapi.DomainHash,
	windowPairs []*externalapi.BlockGHOSTDAGDataHashPair,
	isBlockWithTrustedData bool,
) (uint64, []*externalapi.DomainHash, error) {
	if blockHash.Equal(dm.genesisHash) {
		genesisHeader, err := dm.headerStore.BlockHeader(dm.databaseContext, stagingArea, dm.genesisHash)
		if err != nil {
			return 0, nil, err
		}
		return genesisHeader.DAAScore(), nil, nil
	}

	ghostdagData, err := dm.ghostdagStore.Get(dm.databaseContext, stagingArea, blockHash, false)
	if database.IsNotFoundError(err) {
		log.Infof("calculateBlockWindowHeap failed to retrieve with %s\n", blockHash)
		return 0, nil, err
	}
	if err != nil {
		return 0, nil, err
	}
	mergeSetLength := len(ghostdagData.MergeSetBlues()) + len(ghostdagData.MergeSetReds())
	mergeSet := make(map[externalapi.DomainHash]struct{}, mergeSetLength)
	for _, hash := range ghostdagData.MergeSetBlues() {
		mergeSet[*hash] = struct{}{}
	}

	for _, hash := range ghostdagData.MergeSetReds() {
		mergeSet[*hash] = struct{}{}
	}

	// TODO: Consider optimizing by breaking the loop once you arrive to the
	// window block with blue work higher than all non-added merge set blocks.
	daaAddedBlocks := make([]*externalapi.DomainHash, 0, len(mergeSet))
	for _, pair := range windowPairs {
		hash := pair.Hash
		if _, exists := mergeSet[*hash]; exists {
			daaAddedBlocks = append(daaAddedBlocks, hash)
			if len(daaAddedBlocks) == len(mergeSet) {
				break
			}
		}
	}

	var daaScore uint64
	if isBlockWithTrustedData {
		daaScore, err = dm.daaBlocksStore.DAAScore(dm.databaseContext, stagingArea, blockHash)
		if err != nil {
			return 0, nil, err
		}
	} else {
		selectedParentDAAScore, err := dm.daaBlocksStore.DAAScore(dm.databaseContext, stagingArea, ghostdagData.SelectedParent())
		if err != nil {
			return 0, nil, err
		}
		daaScore = selectedParentDAAScore + uint64(len(daaAddedBlocks))
	}

	return daaScore, daaAddedBlocks, nil
}
