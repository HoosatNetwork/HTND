package difficultymanager

import (
	"math/big"

	"github.com/Hoosat-Oy/HTND/domain/consensus/model"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/infrastructure/logger"
	"github.com/pkg/errors"
)

func (dm *difficultyManager) EstimateNetworkHashesPerSecond(startHash *externalapi.DomainHash, windowSize int) (uint64, error) {
	onEnd := logger.LogAndMeasureExecutionTime(log, "EstimateNetworkHashesPerSecond")
	defer onEnd()

	stagingArea := model.NewStagingArea()
	return dm.estimateNetworkHashesPerSecond(stagingArea, startHash, windowSize)
}

func (dm *difficultyManager) estimateNetworkHashesPerSecond(stagingArea *model.StagingArea,
	startHash *externalapi.DomainHash, windowSize int) (uint64, error) {

	const minWindowSize = 1000
	if windowSize < minWindowSize {
		return 0, errors.Errorf("windowSize must be equal to or greater than %d", minWindowSize)
	}

	blockWindow, err := dm.blockWindow(stagingArea, startHash, windowSize)
	if err != nil {
		return 0, err
	}
	defer blockWindow.free()

	// return 0 if no blocks had been mined yet
	if len(blockWindow.pairs) == 0 {
		return 0, nil
	}

	minWindowTimestamp, maxWindowTimestamp, _ := blockWindow.minMaxTimestamps()
	if minWindowTimestamp == maxWindowTimestamp {
		return 0, errors.Errorf("min window timestamp is equal to the max window timestamp")
	}

	firstBlockBlueWork := blockWindow.pairs[0].GHOSTDAGData.BlueWork()
	minWindowBlueWork := firstBlockBlueWork
	maxWindowBlueWork := firstBlockBlueWork
	for _, pair := range blockWindow.pairs[1:] {
		blockBlueWork := pair.GHOSTDAGData.BlueWork()
		if blockBlueWork.Cmp(minWindowBlueWork) < 0 {
			minWindowBlueWork = blockBlueWork
		}
		if blockBlueWork.Cmp(maxWindowBlueWork) > 0 {
			maxWindowBlueWork = blockBlueWork
		}
	}

	windowsDiff := (maxWindowTimestamp - minWindowTimestamp) / 1000 // Divided by 1000 to convert milliseconds to seconds
	if windowsDiff == 0 {
		return 0, nil
	}

	nominator := new(big.Int).Sub(maxWindowBlueWork, minWindowBlueWork)
	denominator := big.NewInt(windowsDiff)
	networkHashesPerSecondBigInt := new(big.Int).Div(nominator, denominator)
	return networkHashesPerSecondBigInt.Uint64(), nil
}
