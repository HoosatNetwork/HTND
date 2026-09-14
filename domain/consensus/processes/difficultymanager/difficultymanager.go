package difficultymanager

import (
	"math/big"
	"strconv"
	"time"

	"github.com/HoosatNetwork/HTND/infrastructure/logger"
	"github.com/HoosatNetwork/HTND/util/math"

	"github.com/HoosatNetwork/HTND/util/difficulty"

	"github.com/HoosatNetwork/HTND/domain/consensus/database"
	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/constants"
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
	// powScores derives the version whose window size and target time apply to a block (see blockVersion).
	powScores []uint64
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
	powScores []uint64,
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
		powScores:                      powScores,
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

	blockVersion, err := dm.blockVersion(stagingArea, blockHash)
	if err != nil {
		return 0, err
	}
	targetsWindow, err := dm.blockWindow(stagingArea, blockHash, dm.windowSize(blockVersion))
	defer targetsWindow.free()
	if err != nil {
		return 0, err
	}

	err = dm.stageDAAScoreAndAddedBlocks(stagingArea, blockHash, targetsWindow.pairs, isBlockWithTrustedData)
	if err != nil {
		return 0, err
	}

	return dm.requiredDifficultyFromTargetsWindow(targetsWindow, blockVersion)
}

func (dm *difficultyManager) StageDAAData(
	stagingArea *model.StagingArea,
	blockHash *externalapi.DomainHash,
	isBlockWithTrustedData bool,
) error {
	onEnd := logger.LogAndMeasureExecutionTime(log, "StageDAADataAndReturnRequiredDifficulty")
	defer onEnd()

	blockVersion, err := dm.blockVersion(stagingArea, blockHash)
	if err != nil {
		return err
	}
	targetsWindow, err := dm.blockWindow(stagingArea, blockHash, dm.windowSize(blockVersion))
	defer targetsWindow.free()
	if err != nil {
		return err
	}

	err = dm.stageDAAScoreAndAddedBlocks(stagingArea, blockHash, targetsWindow.pairs, isBlockWithTrustedData)
	if err != nil {
		return err
	}

	return nil
}

// RequiredDifficulty returns the difficulty required for some block
func (dm *difficultyManager) RequiredDifficulty(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash) (uint32, error) {
	blockVersion, err := dm.blockVersion(stagingArea, blockHash)
	if err != nil {
		return 0, err
	}
	targetsWindow, err := dm.blockWindow(stagingArea, blockHash, dm.windowSize(blockVersion))
	defer targetsWindow.free()
	if err != nil {
		return 0, err
	}

	return dm.requiredDifficultyFromTargetsWindow(targetsWindow, blockVersion)
}

func (dm *difficultyManager) requiredDifficultyFromTargetsWindow(targetsWindow blockWindow, blockVersion uint16) (uint32, error) {
	if dm.disableDifficultyAdjustment {
		return dm.genesisBits, nil
	}

	// in the past this was < 2 as the comment explains, we changed it to under the window size to
	// make the hashrate(which is ~1.5GH/s) constant in the first 2641 blocks so that we won't have a lot of tips

	// We need at least 2 blocks to get a timestamp interval
	// We could instead clamp the timestamp difference to `targetTimePerBlock`,
	// but then everything will cancel out and we'll get the target from the last block, which will be the same as genesis.
	// We add 64 as a safety margin
	if targetsWindow.len() < 2 || targetsWindow.len() < dm.windowSize(blockVersion) {
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
		Div(newTarget, div.SetInt64(dm.targetTimePerBlock[versionIndex(blockVersion, len(dm.targetTimePerBlock))].Milliseconds()))
	l := max(targetsWindow.len(), 0)
	windowLength, err := strconv.ParseUint(strconv.Itoa(l), 10, 64)
	if err != nil {
		return 0, err
	}
	newTarget.Div(newTarget, div.SetUint64(windowLength))
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

// blockVersion returns the block version whose difficulty window size and target time apply to blockHash.
//
// It is derived from the selected parent's DAA score as this node computed it: the block's own DAA score is computed
// from this very window, so it cannot choose it. Activation scores are far apart, so the selected parent's version
// equals the block's own except at an activation boundary, and it is the same on every node - unlike the
// process-global version this used to read, which depends on the node's uptime and IBD. The global remains only for
// managers built without an activation table and while nothing is known (genesis, trusted-data bootstrap).
func (dm *difficultyManager) blockVersion(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash) (uint16, error) {
	if len(dm.powScores) == 0 {
		return constants.GetBlockVersion(), nil
	}
	ghostdagData, err := dm.ghostdagStore.Get(dm.databaseContext, stagingArea, blockHash, false)
	if database.IsNotFoundError(err) {
		return constants.GetBlockVersion(), nil
	}
	if err != nil {
		return 0, err
	}
	selectedParent := ghostdagData.SelectedParent()
	if selectedParent == nil {
		return 1, nil
	}
	daaScore, err := dm.daaBlocksStore.DAAScore(dm.databaseContext, stagingArea, selectedParent)
	if database.IsNotFoundError(err) {
		return constants.GetBlockVersion(), nil
	}
	if err != nil {
		return 0, err
	}
	return constants.BlockVersionForDAAScore(dm.powScores, daaScore), nil
}

func (dm *difficultyManager) windowSize(blockVersion uint16) int {
	return dm.difficultyAdjustmentWindowSize[versionIndex(blockVersion, len(dm.difficultyAdjustmentWindowSize))]
}

// versionIndex returns the per-version table index for blockVersion, using the last entry for a shorter table.
func versionIndex(blockVersion uint16, length int) int {
	index := max(int(blockVersion)-1, 0)
	if index >= length {
		index = length - 1
	}
	return index
}
