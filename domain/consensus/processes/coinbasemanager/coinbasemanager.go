package coinbasemanager

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/hashset"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/subnetworks"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/transactionhelper"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/txscript"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database"
	"github.com/HoosatNetwork/HTND/v2/util"
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

	// powScores derives the version of a block that has no stored header yet - the block being built - from its
	// staged DAA score (see blockVersion).
	powScores []uint64
}

// mergeSetRewardIgnoresDAAWindowVersion is the block version, activated by a hard fork, from which a
// merge set block's coinbase reward no longer depends on whether that block happened to land inside
// the difficulty-adjustment window (see calcMergedBlockReward). Below this version the historical,
// already-mined-and-accepted behavior is preserved exactly. Shares its activation point with the
// blockVersion >= 10 dev-fee formula change already in ExpectedCoinbaseTransactionInternal - both are
// the same hard fork bucket, activated on mainnet at DAA score 227679830 (see the 9th entry of
// mainnet's POWScores in domain/dagconfig/params.go, added 2026-09-19 - see HTN-216).
const mergeSetRewardIgnoresDAAWindowVersion = 10

// ExpectedCoinbaseTransactionWithAcceptanceData implements model.CoinbaseManager. blockHash always
// names an already-mined block being validated here (never a block still under construction), so
// it always has its own stored header - candidateTimestamp is passed as 0 since blockTimestamp
// will never fall back to it for this caller.
func (c *coinbaseManager) ExpectedCoinbaseTransactionWithAcceptanceData(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash, coinbaseData *externalapi.DomainCoinbaseData, acceptanceData externalapi.AcceptanceData) (expectedTransaction *externalapi.DomainTransaction, hasRedReward bool, err error) {
	return c.ExpectedCoinbaseTransactionInternal(stagingArea, blockHash, coinbaseData, acceptanceData, 0)
}

// ExpectedCoinbaseTransaction implements model.CoinbaseManager. candidateTimestamp must be the
// timestamp the caller is about to commit to blockHash's own header - required only when blockHash
// has no stored header yet (i.e. blockHash names the block currently being built - see
// blockTimestamp), which is always true for this entry point's real callers (blockBuilder).
func (c *coinbaseManager) ExpectedCoinbaseTransaction(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash,
	coinbaseData *externalapi.DomainCoinbaseData, candidateTimestamp int64,
) (expectedTransaction *externalapi.DomainTransaction, hasRedReward bool, err error) {
	acceptanceData, err := c.acceptanceDataStore.Get(c.databaseContext, stagingArea, blockHash)
	if database.IsNotFoundError(err) {
		log.Infof("ExpectedCoinbaseTransaction failed to retrieve with %s\n", blockHash)
		return nil, false, err
	}
	if err != nil {
		return nil, false, err
	}
	return c.ExpectedCoinbaseTransactionInternal(stagingArea, blockHash, coinbaseData, acceptanceData, candidateTimestamp)
}

func (c *coinbaseManager) ExpectedCoinbaseTransactionInternal(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash, coinbaseData *externalapi.DomainCoinbaseData, acceptanceData externalapi.AcceptanceData, candidateTimestamp int64) (expectedTransaction *externalapi.DomainTransaction, hasRedReward bool, err error) {
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

	// blockHash's own version, not the ambient constants.GetBlockVersion(): this function is used to
	// reconstruct/verify a historical block's expected coinbase, potentially long after the node's
	// ambient version has advanced past blockHash's own version (e.g. during a later IBD pass such as
	// ResolveVirtual re-walking history already processed once before). Using the ambient version
	// here doesn't just affect subsidy/entropy - it picks the wrong output-structure model entirely
	// (v1's single bucketed output per blue block vs v2's per-block output with a separate dev-fee
	// output), producing a coinbase with a different output count and no dev fee at all when the
	// block was actually mined under the other model. That's a structural mismatch, not just a value
	// mismatch, and was the confirmed root cause of every UTXO commitment disagreement traced back to
	// this function this session - see ExpectedCoinbaseTransactionInternal's git history.
	ownBlockVersion, err := c.blockVersion(stagingArea, blockHash)
	if err != nil {
		return nil, false, err
	}

	txOuts := make([]*externalapi.DomainTransactionOutput, 0, len(ghostdagData.MergeSetBlues()))
	acceptanceDataMap := acceptanceDataFromArrayToMap(filteredAcceptanceData)
	if ownBlockVersion == 1 {
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
	} else if ownBlockVersion >= 2 {
		log.Tracef("Processing %d blue blocks in merge set", len(ghostdagData.MergeSetBlues()))
		// For v2, process both blue and red blocks individually to avoid bucketing
		// Process all merge set blocks in sorted order for determinism
		//
		// MergeSetBlues()/MergeSetReds() return the GHOSTDAGData's own slices, not copies (and
		// that data is a cached/staged pointer shared with the rest of this block's processing
		// pipeline, and later persisted to disk and the LRU cache as-is). Appending directly onto
		// MergeSetBlues() here would, whenever it has spare capacity, write MergeSetReds() into its
		// backing array in place, and the sort below would then reorder that shared array -
		// corrupting the block's persisted merge set for every future reader. Build a fresh slice
		// instead so this function never mutates GHOSTDAGData's own storage.
		blues := ghostdagData.MergeSetBlues()
		reds := ghostdagData.MergeSetReds()
		allMergeBlocks := make([]*externalapi.DomainHash, 0, len(blues)+len(reds))
		allMergeBlocks = append(allMergeBlocks, blues...)
		allMergeBlocks = append(allMergeBlocks, reds...)
		// Sort merge set blocks by hash to ensure consistent ordering
		sort.Slice(allMergeBlocks, func(i, j int) bool {
			return allMergeBlocks[i].Less(allMergeBlocks[j])
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

		// Which merge set blocks the coinbase pays is consensus, and the two sides answer it about
		// different blocks: the builder asks it of virtual, the validator asks it of the block in
		// front of it. A merge set block that drops out on one side and not the other is exactly an
		// "Output count differs" rejection, and every path below that drops one is silent or traced,
		// so the block and the reason that dropped it are collected and reported together.
		var droppedMergeSetBlocks []string
		for i, blockHash := range allMergeBlocks {
			blockAcc := acceptanceDataMap[*blockHash]
			if blockAcc == nil {
				log.Warnf("No acceptance data found for merge set block %d: %s", i, blockHash)
				droppedMergeSetBlocks = append(droppedMergeSetBlocks,
					fmt.Sprintf("%s (no acceptance data)", blockHash))
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
			mergeSetBlockVersion, err := c.blockVersion(stagingArea, blockHash)
			if err != nil {
				return nil, false, err
			}
			blockReward, err := c.calcMergedBlockReward(stagingArea, blockHash, blockAcc, daaAddedBlocksSet,
				mergeSetBlockVersion >= mergeSetRewardIgnoresDAAWindowVersion)
			if err != nil {
				return nil, false, err
			}
			if blockReward <= 0 {
				log.Tracef("Merge set block %s has no reward", blockHash)
				droppedMergeSetBlocks = append(droppedMergeSetBlocks,
					fmt.Sprintf("%s (no reward; in the DAA added blocks set: %t)", blockHash,
						daaAddedBlocksSet.Contains(blockHash)))
				continue
			}

			// Extract miner's script public key from the block's coinbase transaction
			if len(blockAcc.TransactionAcceptanceData) == 0 || blockAcc.TransactionAcceptanceData[0].Transaction == nil {
				log.Warnf("No coinbase transaction found for merge set block %d: %s", i, blockHash)
				droppedMergeSetBlocks = append(droppedMergeSetBlocks,
					fmt.Sprintf("%s (no coinbase transaction in its acceptance data)", blockHash))
				continue
			}
			_, blockCoinbaseData, _, err := c.ExtractCoinbaseDataBlueScoreAndSubsidyForVersion(
				blockAcc.TransactionAcceptanceData[0].Transaction, mergeSetBlockVersion)
			if err != nil {
				return nil, false, err
			}

			log.Tracef("Block %s: reward=%d, miner=%s, isBlue=%v", blockHash, blockReward, blockCoinbaseData.ScriptPublicKey.String(), isBlue)

			// For both blue and red blocks, use the block's own miner address to stop bucketing
			var minerScript *externalapi.ScriptPublicKey
			minerScript = blockCoinbaseData.ScriptPublicKey

			blockVersion := mergeSetBlockVersion
			var devFee uint64
			if blockVersion >= 10 {
				devFee = calcDevFeeQuantity(blockReward)
			} else {
				devFee = uint64(float64(constants.DevFee) / 100 * float64(blockReward))
			}
			// Calculate dev fee

			blockReward -= devFee
			if blockReward <= 0 {
				droppedMergeSetBlocks = append(droppedMergeSetBlocks,
					fmt.Sprintf("%s (whole reward consumed by the dev fee %d)", blockHash, devFee))
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

		if len(droppedMergeSetBlocks) > 0 {
			log.Infof("Coinbase being built for %s pays nothing to %d of its %d merge set blocks: %s",
				blockHash, len(droppedMergeSetBlocks), len(allMergeBlocks),
				strings.Join(droppedMergeSetBlocks, ", "))
		}

		hasRedReward = len(ghostdagData.MergeSetReds()) > 0
	}

	subsidy, err := c.CalcBlockSubsidy(stagingArea, blockHash, ownBlockVersion)
	if err != nil {
		return nil, false, err
	}

	var entropy [lengthOfEntropy]byte
	if ownBlockVersion >= coinbaseEntropyActivationVersion {
		daaScore, err := c.daaBlocksStore.DAAScore(c.databaseContext, stagingArea, blockHash)
		if err != nil {
			return nil, false, err
		}
		timestamp, err := c.blockTimestamp(stagingArea, blockHash, candidateTimestamp)
		if err != nil {
			return nil, false, err
		}
		entropy = coinbaseEntropy(ghostdagData, daaScore, ownBlockVersion, timestamp)
		log.Tracef("coinbaseEntropy computed for block %s: ownBlockVersion=%d "+
			"(timestampEntropyActive=%t) daaScore=%d candidateTimestamp=%d resolvedTimestamp=%d "+
			"entropy=%x", blockHash, ownBlockVersion, ownBlockVersion >= CoinbaseTimestampEntropyActivationVersion,
			daaScore, candidateTimestamp, timestamp, entropy)
	}

	payload, err := c.serializeCoinbasePayload(ghostdagData.BlueScore(), coinbaseData, subsidy, entropy, ownBlockVersion)
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
//
// blockHash may have no stored header at all: model.VirtualBlockHash/VirtualGenesisBlockHash are
// markers rather than real mined blocks, and a block still under construction (including a test
// harness's temporary placeholder hash) hasn't had its header staged yet either. In every such
// case blockHash necessarily *is* the block currently being built, so the ambient version is the
// correct answer there - it's only wrong when blockHash names a real, already-mined block.
func (c *coinbaseManager) blockVersion(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash) (uint16, error) {
	header, err := c.blockHeaderStore.BlockHeader(c.databaseContext, stagingArea, blockHash)
	if database.IsNotFoundError(err) {
		// The block being built: its version is the one its DAA score gives, which is what the block builder writes
		// into its header and what validation will check. The process-global version this used to return can be
		// ahead of the chain (after IBD, or raised by a relayed header) and made the node build coinbases it rejects.
		if len(c.powScores) > 0 {
			daaScore, daaErr := c.daaBlocksStore.DAAScore(c.databaseContext, stagingArea, blockHash)
			if daaErr == nil {
				return constants.BlockVersionForDAAScore(c.powScores, daaScore), nil
			}
			if !database.IsNotFoundError(daaErr) {
				return 0, daaErr
			}
		}
		return constants.GetBlockVersion(), nil
	}
	if err != nil {
		return 0, err
	}
	return header.Version(), nil
}

// blockTimestamp returns blockHash's own header timestamp (milliseconds), for folding into
// coinbase entropy from CoinbaseTimestampEntropyActivationVersion onward. Mirrors blockVersion's
// handling of a not-yet-built block: blockHash may have no stored header yet (model.VirtualBlockHash
// during block building, or a test harness's temporary placeholder), in which case blockHash
// necessarily *is* the block currently being built, and candidateTimestamp - the timestamp the
// builder is about to commit to that block's own header - is the correct answer instead.
func (c *coinbaseManager) blockTimestamp(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash, candidateTimestamp int64) (int64, error) {
	header, err := c.blockHeaderStore.BlockHeader(c.databaseContext, stagingArea, blockHash)
	if database.IsNotFoundError(err) {
		return candidateTimestamp, nil
	}
	if err != nil {
		return 0, err
	}
	return header.TimeInMilliseconds(), nil
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

// calcDevFeeQuantity returns floor(reward * DevFee / 100) in sompi.
// Multiply-then-divide is exact and platform-independent. The overflow-safe
// form is used so reward * DevFee cannot wrap uint64.
func calcDevFeeQuantity(reward uint64) uint64 {
	fee := uint64(constants.DevFee)
	return (reward/100)*fee + (reward%100)*fee/100
}

// coinbaseOutputForBlueBlock calculates the output that should go into the coinbase transaction of blueBlock
// If blueBlock gets no fee - returns nil for txOut
func (c *coinbaseManager) coinbaseOutputForBlueBlockV2(stagingArea *model.StagingArea,
	blueBlock *externalapi.DomainHash, blockAcceptanceData *externalapi.BlockAcceptanceData,
	mergingBlockDAAAddedBlocksSet hashset.HashSet,
) (*externalapi.DomainTransactionOutput, *externalapi.DomainTransactionOutput, bool, error) {
	blockReward, err := c.calcMergedBlockReward(stagingArea, blueBlock, blockAcceptanceData, mergingBlockDAAAddedBlocksSet, false)
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
	blueBlockVersion, err := c.blockVersion(stagingArea, blueBlock)
	if err != nil {
		return nil, nil, false, err
	}

	// Integer-only split so every node computes the same sompi amounts.
	// floor(blockReward * DevFee / 100); leftover sompi stay with the miner.
	var devFeeQuantity uint64
	if blueBlockVersion >= 10 {
		devFeeQuantity = calcDevFeeQuantity(blockReward)
	} else {
		devFeeQuantity = uint64(float64(constants.DevFee) / 100 * float64(blockReward))
	}
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
	_, coinbaseData, _, err := c.ExtractCoinbaseDataBlueScoreAndSubsidyForVersion(
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
	blockReward, err := c.calcMergedBlockReward(stagingArea, blueBlock, blockAcceptanceData, mergingBlockDAAAddedBlocksSet, false)
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
	_, coinbaseData, _, err := c.ExtractCoinbaseDataBlueScoreAndSubsidyForVersion(
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
		reward, err := c.calcMergedBlockReward(stagingArea, red, acceptanceDataMap[*red], daaAddedBlocksSet, false)
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
		reward, err := c.calcMergedBlockReward(stagingArea, red, acceptanceDataMap[*red], daaAddedBlocksSet, false)
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

// BlockSubsidy is the coinbase subsidy a block at the given DAA score pays, as a pure function of
// the network parameters. It is the same calculation CalcBlockSubsidy performs, without needing a
// consensus instance or a database, so tools can compute expected emission over a range of DAA
// scores rather than reimplementing the schedule and drifting from it.
//
// blockVersion matters: the per-year figures in subsidyByDeflationaryYearTable are per SECOND, and
// are scaled by that version's target time per block. A version targeting 200ms therefore pays a
// fifth of what a version targeting one second pays, for the same emission rate.
func BlockSubsidy(params *dagconfig.Params, blockDAAScore uint64, blockVersion uint16) uint64 {
	if blockDAAScore < params.DeflationaryPhaseDaaScore {
		return params.PreDeflationaryPhaseBaseSubsidy
	}
	return deflationaryPeriodBlockSubsidy(params.TargetTimePerBlock, params.DeflationaryPhaseDaaScore,
		blockDAAScore, blockVersion)
}

func (c *coinbaseManager) calcDeflationaryPeriodBlockSubsidy(blockDaaScore uint64, blockVersion uint16) uint64 {
	return deflationaryPeriodBlockSubsidy(c.targetTimePerBlock, c.deflationaryPhaseDaaScore,
		blockDaaScore, blockVersion)
}

func deflationaryPeriodBlockSubsidy(targetTimePerBlock []time.Duration, deflationaryPhaseDaaScore uint64,
	blockDaaScore uint64, blockVersion uint16,
) uint64 {
	// We define a year as 365.25 days and a month as 365.25 / 12 = 30.4375
	// secondsPerMonth = 30.4375 * 24 * 60 * 60 = 2629800
	// blocksPerYear = 2629800 * 12 / 0.20s (5BPS) = 157788000
	blocksPerYear := uint64(31557600 / targetTimePerBlock[blockVersion-1].Seconds())
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

	yearsSinceDeflationStarted += (blockDaaScore - deflationaryPhaseDaaScore) / blocksPerYear

	// Return the pre-calculated value from subsidy-per-month table
	return deflationaryPeriodBlockSubsidyFromTable(yearsSinceDeflationStarted, targetTimePerBlock, blockVersion)
}

func (c *coinbaseManager) getDeflationaryPeriodBlockSubsidyFromTable(year uint64, blockVersion uint16) uint64 {
	return deflationaryPeriodBlockSubsidyFromTable(year, c.targetTimePerBlock, blockVersion)
}

func deflationaryPeriodBlockSubsidyFromTable(year uint64, targetTimePerBlock []time.Duration,
	blockVersion uint16,
) uint64 {
	if year >= uint64(len(subsidyByDeflationaryYearTable)) {
		maxIdx := len(subsidyByDeflationaryYearTable) - 1
		if maxIdx < 0 {
			panic("subsidyByDeflationaryYearTable is empty")
		}
		// maxIdx is always >= 0, and len() returns int, which is always representable as uint64 on 64-bit platforms
		// Defensive: check only for negative (already checked), so this branch is unreachable
		year = uint64(maxIdx)
	}
	return uint64(float64(subsidyByDeflationaryYearTable[year]) * targetTimePerBlock[blockVersion-1].Seconds())
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

func acceptedFee(txAcceptance *externalapi.TransactionAcceptanceData) uint64 {
	if txAcceptance == nil || !txAcceptance.IsAccepted || txAcceptance.Transaction == nil {
		return 0
	}
	if len(txAcceptance.Transaction.Inputs) == 0 {
		return 0 // coinbase
	}
	// Acceptance data written by older or interrupted resolution paths can be missing the
	// input entries. Keep the recorded fee in that case so block creation does not build a
	// fee-less coinbase that a later complete resolution will reject. When all entries are
	// present, recompute from the UTXOs so a stale recorded Fee cannot create a disagreement.
	if len(txAcceptance.TransactionInputUTXOEntries) != len(txAcceptance.Transaction.Inputs) {
		return txAcceptance.Fee
	}
	var totalIn uint64
	for _, entry := range txAcceptance.TransactionInputUTXOEntries {
		if entry == nil {
			return txAcceptance.Fee
		}
		totalIn += entry.Amount()
	}
	var totalOut uint64
	for _, out := range txAcceptance.Transaction.Outputs {
		totalOut += out.Value
	}
	if totalIn < totalOut {
		return 0
	}
	return totalIn - totalOut
}

// calcMergedBlockReward computes the subsidy+fees a merge set block earns for being merged.
//
// payRegardlessOfDAAWindow=false reproduces the historical behavior exactly (needed to validate
// already-mined blocks under the rules they were mined with): reward is 0 unless blockHash is also
// in mergingBlockDAAAddedBlocksSet - the subset of the merging block's own merge set that happened to
// land inside the (size-bounded, blue-work-ranked) difficulty-adjustment window sample. That window
// exists to pick a representative sample for difficulty adjustment, not to track which merge set
// blocks are "real" - a fully valid, accepted merge set member (blue or red) can lose that sampling
// cutoff for having lower blue work than whatever already filled the window, and before HTN-216 it
// then received no reward at all, silently, for no reason connected to whether it was legitimately
// merged. Measured on a live mainnet node: 27,982 of 27,985 coinbases built over 12 hours dropped at
// least one merge set block this way, one instance dropping 140 of 163.
//
// payRegardlessOfDAAWindow=true (only from mergeSetRewardIgnoresDAAWindowVersion onward) removes that
// condition: every merge set block with valid acceptance data earns its reward, unconditionally.
func (c *coinbaseManager) calcMergedBlockReward(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash,
	blockAcceptanceData *externalapi.BlockAcceptanceData, mergingBlockDAAAddedBlocksSet hashset.HashSet,
	payRegardlessOfDAAWindow bool,
) (uint64, error) {
	if !blockHash.Equal(blockAcceptanceData.BlockHash) {
		return 0, errors.Errorf("blockAcceptanceData.BlockHash is expected to be %s but got %s",
			blockHash, blockAcceptanceData.BlockHash)
	}

	if !payRegardlessOfDAAWindow && !mergingBlockDAAAddedBlocksSet.Contains(blockHash) {
		return 0, nil
	}

	totalFees := uint64(0)
	for _, txAcceptanceData := range blockAcceptanceData.TransactionAcceptanceData {
		totalFees += acceptedFee(txAcceptanceData)
	}

	block, err := c.blockStore.Block(c.databaseContext, stagingArea, blockHash)
	if err != nil {
		return 0, err
	}

	_, _, subsidy, err := c.ExtractCoinbaseDataBlueScoreAndSubsidyForVersion(
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
	powScores []uint64,
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
		powScores:           powScores,
	}
}
