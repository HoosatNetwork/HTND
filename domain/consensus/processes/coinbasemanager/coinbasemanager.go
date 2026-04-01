package coinbasemanager

import (
	"math"
	"time"

	"github.com/Hoosat-Oy/HTND/domain/consensus/model"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/constants"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/hashset"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/subnetworks"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/transactionhelper"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/txscript"
	"github.com/Hoosat-Oy/HTND/infrastructure/db/database"
	"github.com/Hoosat-Oy/HTND/util"
	"github.com/pkg/errors"
)

type coinbaseManager struct {
	subsidyGenesisReward                    uint64
	preDeflationaryPhaseBaseSubsidy         uint64
	coinbasePayloadScriptPublicKeyMaxLength uint8
	genesisHash                             *externalapi.DomainHash
	deflationaryPhaseDaaScore               uint64
	deflationaryPhaseBaseSubsidy            uint64
	deflationaryPhaseCurveFactor            float64
	targetTimePerBlock                      []time.Duration

	databaseContext     model.DBReader
	dagTraversalManager model.DAGTraversalManager
	ghostdagDataStore   model.GHOSTDAGDataStore
	acceptanceDataStore model.AcceptanceDataStore
	daaBlocksStore      model.DAABlocksStore
	blockStore          model.BlockStore
	pruningStore        model.PruningStore
	blockHeaderStore    model.BlockHeaderStore
}

// ExpectedCoinbaseTransactionWithAcceptanceData implements model.CoinbaseManager.
func (c *coinbaseManager) ExpectedCoinbaseTransactionWithAcceptanceData(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash, coinbaseData *externalapi.DomainCoinbaseData, acceptanceData externalapi.AcceptanceData) (expectedTransaction *externalapi.DomainTransaction, hasRedReward bool, err error) {
	return c.ExpectedCoinbaseTransactionInternal(stagingArea, blockHash, coinbaseData, acceptanceData)
}

func (c *coinbaseManager) ExpectedCoinbaseTransaction(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash,
	coinbaseData *externalapi.DomainCoinbaseData,
) (expectedTransaction *externalapi.DomainTransaction, hasRedReward bool, err error) {
	acceptanceData, err := c.acceptanceDataStore.Get(c.databaseContext, stagingArea, blockHash)
	if database.IsNotFoundError(err) {
		log.Infof("ExpectedCoinbaseTransaction failed to retrieve with %s\n", blockHash)
		return nil, false, err
	}
	if err != nil {
		return nil, false, err
	}
	return c.ExpectedCoinbaseTransactionInternal(stagingArea, blockHash, coinbaseData, acceptanceData)
}

func (c *coinbaseManager) ExpectedCoinbaseTransactionInternal(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash, coinbaseData *externalapi.DomainCoinbaseData, acceptanceData externalapi.AcceptanceData) (expectedTransaction *externalapi.DomainTransaction, hasRedReward bool, err error) {
	ghostdagData, err := c.ghostdagDataStore.Get(c.databaseContext, stagingArea, blockHash, true)
	// If there's ghostdag data with trusted data we prefer it because we need the original merge set non-pruned merge set.
	if database.IsNotFoundError(err) {
		ghostdagData, err = c.ghostdagDataStore.Get(c.databaseContext, stagingArea, blockHash, false)
		if err != nil {
			return nil, false, err
		}
	}

	daaAddedBlocksSet, err := c.daaAddedBlocksSet(stagingArea, blockHash)
	if err != nil {
		return nil, false, err
	}

	txOuts := make([]*externalapi.DomainTransactionOutput, 0, len(ghostdagData.MergeSetBlues()))
	acceptanceDataMap := acceptanceDataFromArrayToMap(acceptanceData)
	if constants.GetBlockVersion() == 1 {
		for _, blue := range ghostdagData.MergeSetBlues() {
			txOut, hasReward, err := c.coinbaseOutputForBlueBlockV1(stagingArea, blue, acceptanceDataMap[*blue], daaAddedBlocksSet)
			if err != nil {
				return nil, false, err
			}

			if hasReward {
				txOuts = append(txOuts, txOut)
			}
		}

		txOut, hasRedReward, err := c.coinbaseOutputForRewardFromRedBlocksV1(
			stagingArea, ghostdagData, acceptanceData, daaAddedBlocksSet, coinbaseData)
		if err != nil {
			return nil, false, err
		}

		if hasRedReward {
			txOuts = append(txOuts, txOut)
		}
	} else if constants.GetBlockVersion() >= 2 {
		for _, blue := range ghostdagData.MergeSetBlues() {
			txOut, devTx, hasReward, err := c.coinbaseOutputForBlueBlockV2(stagingArea, blue, acceptanceDataMap[*blue], daaAddedBlocksSet)
			if err != nil {
				return nil, false, err
			}

			if hasReward {
				txOuts = append(txOuts, txOut)
				txOuts = append(txOuts, devTx)
			}
		}

		txOut, devTx, hasRedReward, err := c.coinbaseOutputForRewardFromRedBlocksV2(
			stagingArea, ghostdagData, acceptanceData, daaAddedBlocksSet, coinbaseData)
		if err != nil {
			return nil, false, err
		}

		if hasRedReward {
			txOuts = append(txOuts, txOut)
			txOuts = append(txOuts, devTx)
		}
	}

	subsidy, err := c.CalcBlockSubsidy(stagingArea, blockHash, constants.GetBlockVersion())
	if err != nil {
		return nil, false, err
	}

	payload, err := c.serializeCoinbasePayload(ghostdagData.BlueScore(), coinbaseData, subsidy)
	if err != nil {
		return nil, false, err
	}

	domainTransaction := &externalapi.DomainTransaction{
		Version:      constants.MaxTransactionVersion,
		Inputs:       []*externalapi.DomainTransactionInput{},
		Outputs:      txOuts,
		LockTime:     0,
		SubnetworkID: subnetworks.SubnetworkIDCoinbase,
		Gas:          0,
		Payload:      payload,
	}
	return domainTransaction, hasRedReward, nil
}

func (c *coinbaseManager) daaAddedBlocksSet(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash) (
	hashset.HashSet, error,
) {
	daaAddedBlocks, err := c.daaBlocksStore.DAAAddedBlocks(c.databaseContext, stagingArea, blockHash)
	if err != nil {
		return nil, err
	}

	return hashset.NewFromSlice(daaAddedBlocks...), nil
}

// coinbaseOutputForBlueBlock calculates the output that should go into the coinbase transaction of blueBlock
// If blueBlock gets no fee - returns nil for txOut
func (c *coinbaseManager) coinbaseOutputForBlueBlockV2(stagingArea *model.StagingArea,
	blueBlock *externalapi.DomainHash, blockAcceptanceData *externalapi.BlockAcceptanceData,
	mergingBlockDAAAddedBlocksSet hashset.HashSet,
) (*externalapi.DomainTransactionOutput, *externalapi.DomainTransactionOutput, bool, error) {
	blockReward, err := c.calcMergedBlockReward(stagingArea, blueBlock, blockAcceptanceData, mergingBlockDAAAddedBlocksSet)
	if err != nil {
		return nil, nil, false, err
	}

	devFeeDecodedAddress, err := util.DecodeAddress(constants.DevFeeAddress, util.Bech32PrefixHoosat)
	if err != nil {
		return nil, nil, false, err
	}
	devFeeScriptPublicKey, err := txscript.PayToAddrScript(devFeeDecodedAddress)
	if err != nil {
		return nil, nil, false, err
	}
	devFeeQuantity := uint64(float64(constants.DevFee) / 100 * float64(blockReward))
	blockReward -= devFeeQuantity
	if blockReward <= 0 {
		return nil, nil, false, nil
	}

	// the ScriptPublicKey for the coinbase is parsed from the coinbase payload
	_, coinbaseData, _, err := c.ExtractCoinbaseDataBlueScoreAndSubsidy(blockAcceptanceData.TransactionAcceptanceData[0].Transaction)
	if err != nil {
		return nil, nil, false, err
	}

	txOut := &externalapi.DomainTransactionOutput{
		Value:           blockReward,
		ScriptPublicKey: coinbaseData.ScriptPublicKey,
	}

	devTx := &externalapi.DomainTransactionOutput{
		Value:           devFeeQuantity,
		ScriptPublicKey: devFeeScriptPublicKey,
	}

	return txOut, devTx, true, nil
}

func (c *coinbaseManager) coinbaseOutputForBlueBlockV1(stagingArea *model.StagingArea,
	blueBlock *externalapi.DomainHash, blockAcceptanceData *externalapi.BlockAcceptanceData,
	mergingBlockDAAAddedBlocksSet hashset.HashSet,
) (*externalapi.DomainTransactionOutput, bool, error) {
	blockReward, err := c.calcMergedBlockReward(stagingArea, blueBlock, blockAcceptanceData, mergingBlockDAAAddedBlocksSet)
	if err != nil {
		return nil, false, err
	}

	if blockReward <= 0 {
		return nil, false, nil
	}

	// the ScriptPublicKey for the coinbase is parsed from the coinbase payload
	_, coinbaseData, _, err := c.ExtractCoinbaseDataBlueScoreAndSubsidy(blockAcceptanceData.TransactionAcceptanceData[0].Transaction)
	if err != nil {
		return nil, false, err
	}

	txOut := &externalapi.DomainTransactionOutput{
		Value:           blockReward,
		ScriptPublicKey: coinbaseData.ScriptPublicKey,
	}

	return txOut, true, nil
}

func (c *coinbaseManager) coinbaseOutputForRewardFromRedBlocksV2(stagingArea *model.StagingArea,
	ghostdagData *externalapi.BlockGHOSTDAGData, acceptanceData externalapi.AcceptanceData, daaAddedBlocksSet hashset.HashSet,
	coinbaseData *externalapi.DomainCoinbaseData,
) (*externalapi.DomainTransactionOutput, *externalapi.DomainTransactionOutput, bool, error) {
	acceptanceDataMap := acceptanceDataFromArrayToMap(acceptanceData)
	totalReward := uint64(0)
	for _, red := range ghostdagData.MergeSetReds() {
		if acceptanceDataMap[*red] == nil {
			continue
		}
		reward, err := c.calcMergedBlockReward(stagingArea, red, acceptanceDataMap[*red], daaAddedBlocksSet)
		if err != nil {
			return nil, nil, false, err
		}
		totalReward += reward
	}

	devFeeDecodedAddress, err := util.DecodeAddress(constants.DevFeeAddress, util.Bech32PrefixHoosat)
	if err != nil {
		return nil, nil, false, err
	}
	devFeeScriptPublicKey, err := txscript.PayToAddrScript(devFeeDecodedAddress)
	if err != nil {
		return nil, nil, false, err
	}
	devFeeQuantity := uint64(float64(constants.DevFee) / 100 * float64(totalReward))
	totalReward -= devFeeQuantity
	if totalReward <= 0 {
		return nil, nil, false, nil
	}

	txOut := &externalapi.DomainTransactionOutput{
		Value:           totalReward,
		ScriptPublicKey: coinbaseData.ScriptPublicKey,
	}

	devTx := &externalapi.DomainTransactionOutput{
		Value:           devFeeQuantity,
		ScriptPublicKey: devFeeScriptPublicKey,
	}

	return txOut, devTx, true, nil
}

func (c *coinbaseManager) coinbaseOutputForRewardFromRedBlocksV1(stagingArea *model.StagingArea,
	ghostdagData *externalapi.BlockGHOSTDAGData, acceptanceData externalapi.AcceptanceData, daaAddedBlocksSet hashset.HashSet,
	coinbaseData *externalapi.DomainCoinbaseData,
) (*externalapi.DomainTransactionOutput, bool, error) {
	acceptanceDataMap := acceptanceDataFromArrayToMap(acceptanceData)
	totalReward := uint64(0)
	for _, red := range ghostdagData.MergeSetReds() {
		if acceptanceDataMap[*red] == nil {
			continue
		}
		reward, err := c.calcMergedBlockReward(stagingArea, red, acceptanceDataMap[*red], daaAddedBlocksSet)
		if err != nil {
			return nil, false, err
		}
		totalReward += reward
	}
	if totalReward <= 0 {
		return nil, false, nil
	}

	txOut := &externalapi.DomainTransactionOutput{
		Value:           totalReward,
		ScriptPublicKey: coinbaseData.ScriptPublicKey,
	}

	return txOut, true, nil
}

func acceptanceDataFromArrayToMap(acceptanceData externalapi.AcceptanceData) map[externalapi.DomainHash]*externalapi.BlockAcceptanceData {
	acceptanceDataMap := make(map[externalapi.DomainHash]*externalapi.BlockAcceptanceData, len(acceptanceData))
	for _, blockAcceptanceData := range acceptanceData {
		acceptanceDataMap[*blockAcceptanceData.BlockHash] = blockAcceptanceData
	}
	return acceptanceDataMap
}

// CalcBlockSubsidy returns the subsidy amount a block at the provided blue score
// should have. This is mainly used for determining how much the coinbase for
// newly generated blocks awards as well as validating the coinbase for blocks
// has the expected value.
func (c *coinbaseManager) CalcBlockSubsidy(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash, blockVersion uint16) (uint64, error) {
	if blockHash.Equal(c.genesisHash) {
		return c.subsidyGenesisReward, nil
	}
	blockDaaScore, err := c.daaBlocksStore.DAAScore(c.databaseContext, stagingArea, blockHash)
	if err != nil {
		return 0, err
	}
	if blockDaaScore < c.deflationaryPhaseDaaScore {
		return c.preDeflationaryPhaseBaseSubsidy, nil
	}

	blockSubsidy := c.calcDeflationaryPeriodBlockSubsidy(blockDaaScore, blockVersion)
	return blockSubsidy, nil
}

func (c *coinbaseManager) calcDeflationaryPeriodBlockSubsidy(blockDaaScore uint64, blockVersion uint16) uint64 {
	// We define a year as 365.25 days and a month as 365.25 / 12 = 30.4375
	// secondsPerMonth = 30.4375 * 24 * 60 * 60 = 2629800
	// blocksPerYear = 2629800 * 12 / 0.20s (5BPS) = 157788000
	blocksPerYear := uint64(31557600 / c.targetTimePerBlock[blockVersion-1].Seconds())
	// var blocksPerYear = uint64(31557600)
	// Note that this calculation implicitly assumes that block per second = 1 (by assuming daa score diff is in second units).
	var yearsSinceDeflationStarted uint64
	// First year on 1 BPS
	if blockDaaScore >= 31557600 {
		yearsSinceDeflationStarted = 1
		blockDaaScore -= 31557600
	}
	// Second year partly on 1 BPS, lets bloat the blockDaaScore calculation for those blocks to 5 BPS
	nocturneHfScore := uint64(43334184 - 31557600)
	if blockDaaScore >= nocturneHfScore {
		blockDaaScore += nocturneHfScore * 4
	}

	yearsSinceDeflationStarted += (blockDaaScore - c.deflationaryPhaseDaaScore) / blocksPerYear

	// Return the pre-calculated value from subsidy-per-month table
	return c.getDeflationaryPeriodBlockSubsidyFromTable(yearsSinceDeflationStarted, blockVersion)
}

func (c *coinbaseManager) getDeflationaryPeriodBlockSubsidyFromTable(year uint64, blockVersion uint16) uint64 {
	if year >= uint64(len(subsidyByDeflationaryYearTable)) {
		year = uint64(len(subsidyByDeflationaryYearTable) - 1)
	}
	return uint64(float64(subsidyByDeflationaryYearTable[year]) * c.targetTimePerBlock[blockVersion-1].Seconds())
}

/*
This table was pre-calculated by calling `calcDeflationaryPeriodBlockSubsidyFloatCalc` for all years until reaching 0 subsidy.
To regenerate this table, run `TestBuildSubsidyTable` in coinbasemanager_test.go (note the `deflationaryPhaseBaseSubsidy` therein)
*/
var subsidyByDeflationaryYearTable = []uint64{
	10000000000, 8164965809, 6666666666, 5443310539, 4444444444, 3628873693, 2962962962, 2419249128, 1975308641, 1612832752, 1316872427, 1075221834, 877914951, 716814556, 585276634, 477876371, 390184423, 318584247, 260122948, 212389498, 173415299, 141592998, 115610199, 94395332, 77073466,
	62930221, 51382310, 41953481, 34254873, 27968987, 22836582, 18645991, 15224388, 12430661, 10149592, 8287107, 6766394, 5524738, 4510929, 3683158, 3007286, 2455439, 2004857, 1636959, 1336571, 1091306, 891047, 727537, 594031, 485025,
	396021, 323350, 264014, 215566, 176009, 143711, 117339, 95807, 78226, 63871, 52150, 42581, 34767, 28387, 23178, 18924, 15452, 12616, 10301, 8411, 6867, 5607, 4578, 3738, 3052,
	2492, 2034, 1661, 1356, 1107, 904, 738, 602, 492, 401, 328, 267, 218, 178, 145, 119, 97, 79, 64, 52, 43, 35, 28, 23, 19,
	15, 12, 10, 8, 6, 5, 4, 3, 3, 2, 2, 1, 1, 1, 0,
}

func (c *coinbaseManager) calcDeflationaryPeriodBlockSubsidyFloatCalc(year uint64) uint64 {
	baseSubsidy := c.deflationaryPhaseBaseSubsidy
	curve := c.deflationaryPhaseCurveFactor // default 2
	subsidy := float64(baseSubsidy) / math.Pow(1.5, float64(year)/curve)
	return uint64(subsidy)
}

func (c *coinbaseManager) calcMergedBlockReward(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash,
	blockAcceptanceData *externalapi.BlockAcceptanceData, mergingBlockDAAAddedBlocksSet hashset.HashSet,
) (uint64, error) {
	if !blockHash.Equal(blockAcceptanceData.BlockHash) {
		return 0, errors.Errorf("blockAcceptanceData.BlockHash is expected to be %s but got %s",
			blockHash, blockAcceptanceData.BlockHash)
	}

	if !mergingBlockDAAAddedBlocksSet.Contains(blockHash) {
		return 0, nil
	}

	totalFees := uint64(0)
	for _, txAcceptanceData := range blockAcceptanceData.TransactionAcceptanceData {
		if txAcceptanceData.IsAccepted {
			totalFees += txAcceptanceData.Fee
		}
	}

	block, err := c.blockStore.Block(c.databaseContext, stagingArea, blockHash)
	if err != nil {
		return 0, err
	}

	_, _, subsidy, err := c.ExtractCoinbaseDataBlueScoreAndSubsidy(block.Transactions[transactionhelper.CoinbaseTransactionIndex])
	if err != nil {
		return 0, err
	}

	return subsidy + totalFees, nil
}

// New instantiates a new CoinbaseManager
func New(
	databaseContext model.DBReader,

	subsidyGenesisReward uint64,
	preDeflationaryPhaseBaseSubsidy uint64,
	coinbasePayloadScriptPublicKeyMaxLength uint8,
	genesisHash *externalapi.DomainHash,
	deflationaryPhaseDaaScore uint64,
	deflationaryPhaseBaseSubsidy uint64,
	defaultdeflationaryPhaseCurveFactor float64,
	targetTimePerBlock []time.Duration,
	dagTraversalManager model.DAGTraversalManager,
	ghostdagDataStore model.GHOSTDAGDataStore,
	acceptanceDataStore model.AcceptanceDataStore,
	daaBlocksStore model.DAABlocksStore,
	blockStore model.BlockStore,
	pruningStore model.PruningStore,
	blockHeaderStore model.BlockHeaderStore,
) model.CoinbaseManager {
	return &coinbaseManager{
		databaseContext: databaseContext,

		subsidyGenesisReward:                    subsidyGenesisReward,
		preDeflationaryPhaseBaseSubsidy:         preDeflationaryPhaseBaseSubsidy,
		coinbasePayloadScriptPublicKeyMaxLength: coinbasePayloadScriptPublicKeyMaxLength,
		genesisHash:                             genesisHash,
		deflationaryPhaseDaaScore:               deflationaryPhaseDaaScore,
		deflationaryPhaseBaseSubsidy:            deflationaryPhaseBaseSubsidy,
		deflationaryPhaseCurveFactor:            defaultdeflationaryPhaseCurveFactor,
		targetTimePerBlock:                      targetTimePerBlock,

		dagTraversalManager: dagTraversalManager,
		ghostdagDataStore:   ghostdagDataStore,
		acceptanceDataStore: acceptanceDataStore,
		daaBlocksStore:      daaBlocksStore,
		blockStore:          blockStore,
		pruningStore:        pruningStore,
		blockHeaderStore:    blockHeaderStore,
	}
}
