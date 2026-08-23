package coinbasemanager

import (
	"math"
	"sort"
	"time"

	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/hashset"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/subnetworks"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/transactionhelper"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/txscript"
	"github.com/HoosatNetwork/HTND/infrastructure/db/database"
	"github.com/HoosatNetwork/HTND/util"
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
	ghostdagData, err := c.ghostdagDataStore.Get(c.databaseContext, stagingArea, blockHash, false)
	// If there's ghostdag data with trusted data we prefer it because we need the original merge set non-pruned merge set.
	if database.IsNotFoundError(err) {
		ghostdagData, err = c.ghostdagDataStore.Get(c.databaseContext, stagingArea, blockHash, true)
		if err != nil {
			return nil, false, err
		}
	}
	log.Tracef("ExpectedCoinbaseTransactionInternal: acceptanceData has %d blocks, GHOSTDAG merge set has %d blues, %d reds", len(acceptanceData), len(ghostdagData.MergeSetBlues()), len(ghostdagData.MergeSetReds()))

	// Filter acceptance data to only include blocks in the merge set
	// This ensures we only process blocks that are actually in the merge set
	// Build a set of block hashes that are in the merge set
	mergeSetHashes := make(map[string]bool)
	for _, blockHash := range ghostdagData.MergeSetBlues() {
		mergeSetHashes[blockHash.String()] = true
	}
	for _, blockHash := range ghostdagData.MergeSetReds() {
		mergeSetHashes[blockHash.String()] = true
	}

	// Filter the acceptance data to only include blocks in the merge set
	filteredAcceptanceData := make(externalapi.AcceptanceData, 0, len(acceptanceData))
	for _, blockAcceptance := range acceptanceData {
		if blockAcceptance.BlockHash != nil {
			if mergeSetHashes[blockAcceptance.BlockHash.String()] {
				filteredAcceptanceData = append(filteredAcceptanceData, blockAcceptance)
			}
		}
	}
	log.Tracef("Filtered acceptance data from %d blocks to %d blocks (merge set only)", len(acceptanceData), len(filteredAcceptanceData))

	daaAddedBlocksSet, err := c.daaAddedBlocksSet(stagingArea, blockHash)
	if err != nil {
		return nil, false, err
	}

	txOuts := make([]*externalapi.DomainTransactionOutput, 0, len(ghostdagData.MergeSetBlues()))
	acceptanceDataMap := acceptanceDataFromArrayToMap(filteredAcceptanceData)
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
		log.Tracef("Processing %d blue blocks in merge set", len(ghostdagData.MergeSetBlues()))
		// For v2, process both blue and red blocks individually to avoid bucketing
		// Process all merge set blocks in sorted order for determinism
		allMergeBlocks := append(ghostdagData.MergeSetBlues(), ghostdagData.MergeSetReds()...)
		// Sort merge set blocks by hash to ensure consistent ordering
		sort.Slice(allMergeBlocks, func(i, j int) bool {
			return allMergeBlocks[i].String() < allMergeBlocks[j].String()
		})
		log.Tracef("Processing %d total merge set blocks (blues + reds)", len(allMergeBlocks))

		devFeeDecodedAddress, err := util.DecodeAddress(constants.DevFeeAddress, util.Bech32PrefixHoosat)
		if err != nil {
			return nil, false, err
		}
		devFeeScriptPublicKey, err := txscript.PayToAddrScript(devFeeDecodedAddress)
		if err != nil {
			return nil, false, err
		}

		for i, blockHash := range allMergeBlocks {
			blockAcc := acceptanceDataMap[*blockHash]
			if blockAcc == nil {
				log.Warnf("No acceptance data found for merge set block %d: %s", i, blockHash)
				continue
			}
			log.Tracef("Processing merge set block %d: %s", i, blockHash)

			// Check if this is a blue block (in MergeSetBlues)
			isBlue := false
			for _, b := range ghostdagData.MergeSetBlues() {
				if b.Equal(blockHash) {
					isBlue = true
					break
				}
			}

			// Get reward and miner script
			blockReward, err := c.calcMergedBlockReward(stagingArea, blockHash, blockAcc, daaAddedBlocksSet)
			if err != nil {
				return nil, false, err
			}
			if blockReward <= 0 {
				log.Tracef("Merge set block %s has no reward", blockHash)
				continue
			}

			// Extract miner's script public key from the block's coinbase transaction
			if len(blockAcc.TransactionAcceptanceData) == 0 || blockAcc.TransactionAcceptanceData[0].Transaction == nil {
				log.Warnf("No coinbase transaction found for merge set block %d: %s", i, blockHash)
				continue
			}
			mergeSetBlockVersion, err := c.blockVersion(stagingArea, blockHash)
			if err != nil {
				return nil, false, err
			}
			_, blockCoinbaseData, _, err := c.extractCoinbaseDataBlueScoreAndSubsidyForVersion(
				blockAcc.TransactionAcceptanceData[0].Transaction, mergeSetBlockVersion)
			if err != nil {
				return nil, false, err
			}

			log.Tracef("Block %s: reward=%d, miner=%s, isBlue=%v", blockHash, blockReward, blockCoinbaseData.ScriptPublicKey.String(), isBlue)

			// For both blue and red blocks, use the block's own miner address to stop bucketing
			var minerScript *externalapi.ScriptPublicKey
			minerScript = blockCoinbaseData.ScriptPublicKey

			// Calculate dev fee
			devFee := uint64(float64(constants.DevFee) / 100 * float64(blockReward))
			blockReward -= devFee
			if blockReward <= 0 {
				continue
			}

			// Create reward output
			txOut := &externalapi.DomainTransactionOutput{
				Value:           blockReward,
				ScriptPublicKey: minerScript,
			}
			// Create dev fee output
			devTx := &externalapi.DomainTransactionOutput{
				Value:           devFee,
				ScriptPublicKey: devFeeScriptPublicKey,
			}

			txOuts = append(txOuts, txOut)
			txOuts = append(txOuts, devTx)
		}

		hasRedReward = len(ghostdagData.MergeSetReds()) > 0
	}

	subsidy, err := c.CalcBlockSubsidy(stagingArea, blockHash, constants.GetBlockVersion())
	if err != nil {
		return nil, false, err
	}

	var entropy [lengthOfEntropy]byte
	if constants.GetBlockVersion() >= coinbaseEntropyActivationVersion {
		daaScore, err := c.daaBlocksStore.DAAScore(c.databaseContext, stagingArea, blockHash)
		if err != nil {
			return nil, false, err
		}
		entropy = coinbaseEntropy(ghostdagData, daaScore)
	}

	payload, err := c.serializeCoinbasePayload(ghostdagData.BlueScore(), coinbaseData, subsidy, entropy)
	if err != nil {
		return nil, false, err
	}

	log.Tracef("ExpectedCoinbaseTransactionInternal: created %d outputs", len(txOuts))
	for i, out := range txOuts {
		log.Tracef("  Expected output %d: value=%d, script=%s", i, out.Value, out.ScriptPublicKey.String())
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

// blockVersion returns blockHash's own header version. Used when parsing a coinbase
// transaction that doesn't belong to the block currently being built/validated (e.g.
// a merge-set block's coinbase, while computing another block's reward split), since
// the ambient constants.GetBlockVersion() reflects that other, currently-processed
// block instead.
func (c *coinbaseManager) blockVersion(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash) (uint16, error) {
	header, err := c.blockHeaderStore.BlockHeader(c.databaseContext, stagingArea, blockHash)
	if err != nil {
		return 0, err
	}
	return header.Version(), nil
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
	// For each blue block, extract the miner's address from that block's coinbase transaction
	if len(blockAcceptanceData.TransactionAcceptanceData) == 0 || blockAcceptanceData.TransactionAcceptanceData[0].Transaction == nil {
		log.Warnf("coinbaseOutputForBlueBlockV2: no coinbase transaction found in acceptance data for block %s", blueBlock)
		return nil, nil, false, nil
	}
	blueBlockVersion, err := c.blockVersion(stagingArea, blueBlock)
	if err != nil {
		return nil, nil, false, err
	}
	_, coinbaseData, _, err := c.extractCoinbaseDataBlueScoreAndSubsidyForVersion(
		blockAcceptanceData.TransactionAcceptanceData[0].Transaction, blueBlockVersion)
	if err != nil {
		return nil, nil, false, err
	}

	log.Tracef("coinbaseOutputForBlueBlockV2: blue block %s, reward=%d, miner script=%s", blueBlock, blockReward, coinbaseData.ScriptPublicKey.String())

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
	blueBlockVersion, err := c.blockVersion(stagingArea, blueBlock)
	if err != nil {
		return nil, false, err
	}
	_, coinbaseData, _, err := c.extractCoinbaseDataBlueScoreAndSubsidyForVersion(
		blockAcceptanceData.TransactionAcceptanceData[0].Transaction, blueBlockVersion)
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
		maxIdx := len(subsidyByDeflationaryYearTable) - 1
		if maxIdx < 0 {
			panic("subsidyByDeflationaryYearTable is empty")
		}
		// maxIdx is always >= 0, and len() returns int, which is always representable as uint64 on 64-bit platforms
		// Defensive: check only for negative (already checked), so this branch is unreachable
		year = uint64(maxIdx)
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

	_, _, subsidy, err := c.extractCoinbaseDataBlueScoreAndSubsidyForVersion(
		block.Transactions[transactionhelper.CoinbaseTransactionIndex], block.Header.Version())
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
