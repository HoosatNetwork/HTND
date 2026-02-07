package blockvalidator

import (
	"encoding/json"

	"github.com/Hoosat-Oy/HTND/domain/consensus/model"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/ruleerrors"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/constants"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/transactionhelper"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/txscript"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/virtual"
	"github.com/Hoosat-Oy/HTND/domain/dagconfig"
	"github.com/Hoosat-Oy/HTND/infrastructure/logger"
	"github.com/pkg/errors"
)

// ValidateBodyInContext validates block bodies in the context of the current
// consensus state
func (v *blockValidator) ValidateBodyInContext(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash, isBlockWithTrustedData bool) error {
	onEnd := logger.LogAndMeasureExecutionTime(log, "ValidateBodyInContext")
	defer onEnd()

	block, err := v.blockStore.Block(v.databaseContext, stagingArea, blockHash)
	if err != nil {
		return err
	}

	if !isBlockWithTrustedData {
		err := v.checkBlockIsNotPruned(stagingArea, blockHash)
		if err != nil {
			return err
		}
	}

	err = v.checkBlockTransactions(stagingArea, blockHash, block)
	if err != nil {
		return err
	}

	if !isBlockWithTrustedData {
		err := v.checkParentBlockBodiesExist(stagingArea, blockHash)
		if err != nil {
			return err
		}
		reward, err := v.checkCoinbaseSubsidy(stagingArea, blockHash, block)
		if err != nil {
			return err
		}

		err = v.checkDevFee(stagingArea, block, reward)
		if err != nil {
			return err
		}

	}
	return nil
}

// checkBlockIsNotPruned Checks we don't add block bodies to pruned blocks
func (v *blockValidator) checkBlockIsNotPruned(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash) error {
	hasValidatedHeader, err := v.hasValidatedHeader(stagingArea, blockHash)
	if err != nil {
		return err
	}

	// If we don't add block body to a header only block it can't be in the past
	// of the tips, because it'll be a new tip.
	if !hasValidatedHeader {
		return nil
	}

	tips, err := v.consensusStateStore.Tips(stagingArea, v.databaseContext)
	if err != nil {
		return err
	}

	isAncestorOfSomeTips, err := v.dagTopologyManagers[0].IsAncestorOfAny(stagingArea, blockHash, tips)
	if err != nil {
		return err
	}

	// A header only block in the past of one of the tips has to be pruned
	if isAncestorOfSomeTips {
		return errors.Wrapf(ruleerrors.ErrPrunedBlock, "cannot add block body to a pruned block %s", blockHash)
	}

	return nil
}

func (v *blockValidator) checkParentBlockBodiesExist(
	stagingArea *model.StagingArea, blockHash *externalapi.DomainHash) error {

	missingParentHashes := []*externalapi.DomainHash{}
	parents, err := v.dagTopologyManagers[0].Parents(stagingArea, blockHash)
	if err != nil {
		return err
	}

	if virtual.ContainsOnlyVirtualGenesis(parents) {
		return nil
	}

	for _, parent := range parents {
		hasBlock, err := v.blockStore.HasBlock(v.databaseContext, stagingArea, parent)
		if err != nil {
			return err
		}

		if !hasBlock {
			pruningPoint, err := v.pruningStore.PruningPoint(v.databaseContext, stagingArea)
			if err != nil {
				return err
			}

			isInPastOfPruningPoint, err := v.dagTopologyManagers[0].IsAncestorOf(stagingArea, parent, pruningPoint)
			if err != nil {
				return err
			}

			// If a block parent is in the past of the pruning point
			// it means its body will never be used, so it's ok if
			// it's missing.
			// This will usually happen during IBD when getting the blocks
			// in the pruning point anticone.
			if isInPastOfPruningPoint {
				log.Debugf("Block %s parent %s is missing a body, but is in the past of the pruning point",
					blockHash, parent)
				continue
			}

			log.Debugf("Block %s parent %s is missing a body", blockHash, parent)

			missingParentHashes = append(missingParentHashes, parent)
		}
	}

	if len(missingParentHashes) > 0 {
		return ruleerrors.NewErrMissingParents(missingParentHashes)
	}

	return nil
}

func (v *blockValidator) checkBlockTransactions(
	stagingArea *model.StagingArea, blockHash *externalapi.DomainHash, block *externalapi.DomainBlock) error {

	// Ensure all transactions in the block are finalized.
	pastMedianTime, err := v.pastMedianTimeManager.PastMedianTime(stagingArea, blockHash)
	if err != nil {
		return err
	}
	for _, tx := range block.Transactions {
		if err = v.transactionValidator.ValidateTransactionInContextIgnoringUTXO(stagingArea, tx, blockHash, pastMedianTime, block.Header.DAAScore()); err != nil {
			return err
		}
	}

	return nil
}

func (v *blockValidator) checkCoinbaseSubsidy(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash, block *externalapi.DomainBlock) (uint64, error) {

	expectedSubsidy, err := v.coinbaseManager.CalcBlockSubsidy(stagingArea, blockHash, block.Header.Version())
	if err != nil {
		return 0, err
	}

	_, _, subsidy, err := v.coinbaseManager.ExtractCoinbaseDataBlueScoreAndSubsidy(block.Transactions[transactionhelper.CoinbaseTransactionIndex])
	if err != nil {
		return 0, err
	}

	if block.Header.DAAScore() <= 43334184 || 43334184+10000000 <= block.Header.DAAScore() {
		if subsidy != expectedSubsidy {
			return 0, errors.Wrapf(ruleerrors.ErrWrongCoinbaseSubsidy, "the subsidy specified on the coinbase of %s is "+
				"wrong: expected %d but got %d, blocks version %d", blockHash, expectedSubsidy, subsidy, block.Header.Version())
		}
	} else {
		minSubsidy := expectedSubsidy / 2
		maxSubsidy := expectedSubsidy * 2
		if minSubsidy > subsidy || subsidy > maxSubsidy {
			return 0, errors.Wrapf(ruleerrors.ErrWrongCoinbaseSubsidy, "the subsidy specified on the coinbase of %s is "+
				"out of range: expected between %d and %d but got %d, blocks version %d", blockHash, minSubsidy, maxSubsidy, subsidy, block.Header.Version())
		}
	}

	return subsidy, nil
}

func IsDevFeeOutput(reward uint64, block *externalapi.DomainBlock, output *externalapi.DomainTransactionOutput) bool {
	_, address, err := txscript.ExtractScriptPubKeyAddress(output.ScriptPublicKey, &dagconfig.MainnetParams)
	if err != nil {
		return false
	}
	devFeeAddressInBlock := address.EncodeAddress()
	isDevFeeAddressEqual := devFeeAddressInBlock == constants.DevFeeAddress
	devFeeMinQuantity := uint64(float64(constants.DevFeeMin) / 100 * float64(reward))
	isValueEqual := output.Value >= devFeeMinQuantity
	return isDevFeeAddressEqual && isValueEqual
}

func (v *blockValidator) checkDevFee(stagingArea *model.StagingArea, block *externalapi.DomainBlock, reward uint64) error {
	if block.Header.Version() < 2 || block.Transactions[0].Version == 0 {
		return nil
	}
	// Check for nodeFee in block outputs
	if len(block.Transactions) < 1 {
		jsonBytes, _ := json.MarshalIndent(block, "", "    ")
		return errors.Wrapf(ruleerrors.ErrDevFeeNotIncluded, "transactions do not include dev fee transaction. \n%s", string(jsonBytes))
	}
	if len(block.Transactions[0].Outputs) < 1 {
		jsonBytes, _ := json.MarshalIndent(block, "", "    ")
		return errors.Wrapf(ruleerrors.ErrDevFeeNotIncluded, "transactions do not include dev fee transaction. \n%s", string(jsonBytes))
	}

	hasDevFee := false
	for _, transaction := range block.Transactions {
		for _, output := range transaction.Outputs {
			if IsDevFeeOutput(reward, block, output) {
				hasDevFee = true
				break
			}
		}
		if hasDevFee {
			break
		}
	}

	if !hasDevFee {
		jsonBytes, _ := json.MarshalIndent(block, "", "    ")
		return errors.Wrapf(ruleerrors.ErrDevFeeNotIncluded, "transactions do not include dev fee transaction. \n%s", string(jsonBytes))
	}
	return nil
}
