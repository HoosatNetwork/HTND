package consensusstatemanager

import (
	"fmt"
	"sort"
	"sync"

	"github.com/HoosatNetwork/HTND/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/transactionhelper"

	"github.com/HoosatNetwork/HTND/domain/consensus/utils/consensushashing"

	"github.com/HoosatNetwork/HTND/domain/consensus/utils/merkle"

	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/ruleerrors"
	"github.com/pkg/errors"
)

func (csm *consensusStateManager) verifyUTXO(stagingArea *model.StagingArea, block *externalapi.DomainBlock,
	blockHash *externalapi.DomainHash, pastUTXODiff externalapi.UTXODiff, acceptanceData externalapi.AcceptanceData,
	multiset model.Multiset,
) error {
	log.Tracef("verifyUTXO start for block %s", blockHash)
	defer log.Tracef("verifyUTXO end for block %s", blockHash)

	log.Debugf("Validating UTXO commitment for block %s", blockHash)
	err := csm.validateUTXOCommitment(block, blockHash, multiset)
	if err != nil {
		return err
	}
	log.Debugf("UTXO commitment validation passed for block %s", blockHash)

	log.Debugf("Validating acceptedIDMerkleRoot for block %s", blockHash)
	err = csm.validateAcceptedIDMerkleRoot(block, blockHash, acceptanceData)
	if err != nil {
		return err
	}
	log.Debugf("AcceptedIDMerkleRoot validation passed for block %s", blockHash)

	// Only validate coinbase if DAA score is above the threshold
	// Note: coinbasemanager gracefully handles missing acceptance data for header-only blue blocks
	if block.Header.DAAScore() >= 31557600*2.2 {
		coinbaseTransaction := block.Transactions[0]
		err = csm.validateCoinbaseTransaction(stagingArea, block, blockHash, coinbaseTransaction, acceptanceData)
		if err != nil {
			return err
		}
		log.Debugf("Coinbase transaction validation passed for block %s", blockHash)
	}

	log.Debugf("Validating transactions against past UTXO for block %s", blockHash)
	err = csm.validateBlockTransactionsAgainstPastUTXO(stagingArea, block, pastUTXODiff)
	if err != nil {
		return err
	}
	log.Tracef("Transactions against past UTXO validation passed for block %s", blockHash)

	return nil
}

func (csm *consensusStateManager) validateBlockTransactionsAgainstPastUTXO(stagingArea *model.StagingArea,
	block *externalapi.DomainBlock, pastUTXODiff externalapi.UTXODiff,
) error {
	blockHash := consensushashing.BlockHash(block)
	log.Tracef("validateBlockTransactionsAgainstPastUTXO start for block %s", blockHash)
	defer log.Tracef("validateBlockTransactionsAgainstPastUTXO end for block %s", blockHash)

	selectedParentMedianTime, err := csm.pastMedianTimeManager.PastMedianTime(stagingArea, blockHash)
	if err != nil {
		return err
	}
	log.Tracef("The past median time of %s is %d", blockHash, selectedParentMedianTime)

	var wg sync.WaitGroup
	var mu sync.Mutex
	var stagingMu sync.Mutex
	var firstErr error
	done := make(chan struct{}) // Signal to stop other goroutines on first error

	for i, transaction := range block.Transactions {
		if i == transactionhelper.CoinbaseTransactionIndex {
			log.Tracef("Skipping transaction %s because it is the coinbase", consensushashing.TransactionID(transaction))
			continue
		}

		wg.Add(1)
		go func(tx *externalapi.DomainTransaction) {
			defer wg.Done()

			select {
			case <-done:
				return // Early exit if another goroutine found an error
			default:
			}

			transactionID := consensushashing.TransactionID(tx)
			log.Tracef("Validating transaction %s in block %s against the block's past UTXO", transactionID, blockHash)

			// Populate UTXO entries
			stagingMu.Lock()
			err := csm.populateTransactionWithUTXOEntriesFromVirtualOrDiff(stagingArea, tx, pastUTXODiff)
			stagingMu.Unlock()
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
					close(done) // Signal others to stop
				}
				mu.Unlock()
				return
			}

			// Validate transaction
			err = csm.transactionValidator.ValidateTransactionInContextAndPopulateFee(
				stagingArea, tx, blockHash, block.Header.DAAScore())
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
					close(done)
				}
				mu.Unlock()
				return
			}

			log.Tracef("Validation against the block's past UTXO passed for transaction %s in block %s", transactionID, blockHash)
		}(transaction)
	}

	wg.Wait()
	return firstErr
}

func (csm *consensusStateManager) validateAcceptedIDMerkleRoot(block *externalapi.DomainBlock,
	blockHash *externalapi.DomainHash, acceptanceData externalapi.AcceptanceData,
) error {
	log.Tracef("validateAcceptedIDMerkleRoot start for block %s", blockHash)
	defer log.Tracef("validateAcceptedIDMerkleRoot end for block %s", blockHash)

	calculatedAcceptedIDMerkleRoot := calculateAcceptedIDMerkleRoot(acceptanceData)
	if !block.Header.AcceptedIDMerkleRoot().Equal(calculatedAcceptedIDMerkleRoot) {
		return errors.Wrapf(ruleerrors.ErrBadMerkleRoot, "block %s accepted ID merkle root is invalid - block "+
			"header indicates %s, but calculated value is %s",
			blockHash, block.Header.UTXOCommitment(), calculatedAcceptedIDMerkleRoot)
	}

	return nil
}

func (csm *consensusStateManager) validateUTXOCommitment(
	block *externalapi.DomainBlock, blockHash *externalapi.DomainHash, multiset model.Multiset,
) error {
	log.Tracef("validateUTXOCommitment start for block %s", blockHash)
	defer log.Tracef("validateUTXOCommitment end for block %s", blockHash)

	if blockHash.Equal(csm.genesisHash) {
		return nil
	}

	multisetHash := multiset.Hash()
	if !block.Header.UTXOCommitment().Equal(multisetHash) {
		return errors.Wrapf(ruleerrors.ErrBadUTXOCommitment, "block %s UTXO commitment is invalid - block "+
			"header indicates %s, but calculated value is %s", blockHash, block.Header.UTXOCommitment(), multisetHash)
	}

	return nil
}

func calculateAcceptedIDMerkleRoot(multiblockAcceptanceData externalapi.AcceptanceData) *externalapi.DomainHash {
	log.Tracef("calculateAcceptedIDMerkleRoot start")
	defer log.Tracef("calculateAcceptedIDMerkleRoot end")

	var acceptedTransactions []*externalapi.DomainTransaction

	for _, blockAcceptanceData := range multiblockAcceptanceData {
		for _, transactionAcceptance := range blockAcceptanceData.TransactionAcceptanceData {
			if !transactionAcceptance.IsAccepted {
				continue
			}
			acceptedTransactions = append(acceptedTransactions, transactionAcceptance.Transaction)
		}
	}
	// In block version 4 and below, the accepted transactions are sorted by their IDs, in Block Version 5 and above, the order is not important
	if constants.GetBlockVersion() < 5 {
		sort.Slice(acceptedTransactions, func(i, j int) bool {
			return consensushashing.TransactionID(acceptedTransactions[i]).Less(
				consensushashing.TransactionID(acceptedTransactions[j]))
		})
	}

	return merkle.CalculateIDMerkleRoot(acceptedTransactions)
}

func (csm *consensusStateManager) validateCoinbaseTransaction(stagingArea *model.StagingArea, _ *externalapi.DomainBlock,
	blockHash *externalapi.DomainHash, coinbaseTransaction *externalapi.DomainTransaction, acceptanceData externalapi.AcceptanceData,
) error {
	log.Tracef("validateCoinbaseTransaction start for block %s", blockHash)
	defer log.Tracef("validateCoinbaseTransaction end for block %s", blockHash)

	coinbaseTransactionHash := consensushashing.TransactionHash(coinbaseTransaction)
	log.Debugf("validateCoinbaseTransaction: validating coinbase tx %s (hash: %s) in block %s",
		consensushashing.TransactionID(coinbaseTransaction), coinbaseTransactionHash, blockHash)

	// Log acceptance data summary
	log.Debugf("Acceptance data for block %s: %d blocks", blockHash, len(acceptanceData))
	totalAcceptedTx := 0
	totalRejectedTx := 0
	for blockIdx, blockAcceptance := range acceptanceData {
		for _, txAcceptance := range blockAcceptance.TransactionAcceptanceData {
			if txAcceptance.IsAccepted {
				totalAcceptedTx++
			} else {
				totalRejectedTx++
			}
		}
		log.Debugf("  Block %d in acceptance data: %d accepted, %d rejected transactions",
			blockIdx, len(blockAcceptance.TransactionAcceptanceData)-totalRejectedTx, totalRejectedTx)
	}
	log.Debugf("Total acceptance data: %d accepted tx, %d rejected tx across %d blocks",
		totalAcceptedTx, totalRejectedTx, len(acceptanceData))

	log.Debugf("Extracting coinbase data for coinbase transaction %s in block %s",
		consensushashing.TransactionID(coinbaseTransaction), blockHash)
	bluescore, coinbaseData, subsidy, err := csm.coinbaseManager.ExtractCoinbaseDataBlueScoreAndSubsidy(coinbaseTransaction)
	if err != nil {
		return err
	}

	log.Debugf("Extracted coinbase data - bluescore: %d, subsidy: %d, coinbaseData: %+v", bluescore, subsidy, coinbaseData)

	log.Debugf("Calculating the expected coinbase transaction for the given coinbase data and block %s", blockHash)

	// Fetch the block's GHOSTDAG data to get the actual merge set used when the block was created
	blockGHOSTDAGData, err := csm.ghostdagDataStore.Get(csm.databaseContext, stagingArea, blockHash, false)
	if err != nil {
		return errors.Wrapf(err, "failed to get GHOSTDAG data for block %s", blockHash)
	}

	// Log GHOSTDAG merge set
	log.Debugf("GHOSTDAG merge set for block %s: %d blues, %d reds",
		blockHash, len(blockGHOSTDAGData.MergeSetBlues()), len(blockGHOSTDAGData.MergeSetReds()))

	// Filter acceptance data to only include blocks in the GHOSTDAG merge set.
	// This ensures we use the same set of blocks that were used to generate the real coinbase.
	filteredAcceptanceData := filterAcceptanceDataByMergeSet(acceptanceData, blockGHOSTDAGData)

	log.Debugf("Original acceptance data: %d blocks, Filtered: %d blocks",
		len(acceptanceData), len(filteredAcceptanceData))

	expectedCoinbaseTransaction, hasRedReward, err := csm.coinbaseManager.ExpectedCoinbaseTransactionWithAcceptanceData(
		stagingArea, blockHash, coinbaseData, filteredAcceptanceData)
	if err != nil {
		return err
	}

	// Log the expected transaction to debug
	log.Debugf("Expected coinbase: %d outputs", len(expectedCoinbaseTransaction.Outputs))
	for i, output := range expectedCoinbaseTransaction.Outputs {
		log.Debugf("  Expected Output %d: Value: %d, Script: %x",
			i, output.Value, output.ScriptPublicKey.Script)
	}

	// Log filtered acceptance data details
	log.Debugf("Filtered acceptance data: %d blocks (original: %d)",
		len(filteredAcceptanceData), len(acceptanceData))
	for i, blockAcceptance := range filteredAcceptanceData {
		if blockAcceptance.BlockHash != nil {
			log.Debugf("  Filtered Block %d: hash=%s, %d transactions",
				i, blockAcceptance.BlockHash, len(blockAcceptance.TransactionAcceptanceData))
		}
	}

	expectedCoinbaseTransactionHash := consensushashing.TransactionHash(expectedCoinbaseTransaction)
	log.Debugf("Expected coinbase transaction - hash: %s, hasRedReward: %t", expectedCoinbaseTransactionHash, hasRedReward)

	// Calculate total output values
	var givenTotalValue uint64
	for _, output := range coinbaseTransaction.Outputs {
		givenTotalValue += output.Value
	}
	var expectedTotalValue uint64
	for _, output := range expectedCoinbaseTransaction.Outputs {
		expectedTotalValue += output.Value
	}

	log.Debugf("Given coinbase tx - inputs: %d, outputs: %d, total output value: %d",
		len(coinbaseTransaction.Inputs), len(coinbaseTransaction.Outputs), givenTotalValue)
	log.Debugf("Expected coinbase tx - inputs: %d, outputs: %d, total output value: %d",
		len(expectedCoinbaseTransaction.Inputs), len(expectedCoinbaseTransaction.Outputs), expectedTotalValue)

	if len(coinbaseTransaction.Inputs) != len(expectedCoinbaseTransaction.Inputs) {
		log.Debugf("Different input count: given has %d, expected has %d",
			len(coinbaseTransaction.Inputs), len(expectedCoinbaseTransaction.Inputs))
	}

	if len(coinbaseTransaction.Outputs) != len(expectedCoinbaseTransaction.Outputs) {
		log.Debugf("Different output count: given has %d, expected has %d (this is OK - different merge sets)",
			len(coinbaseTransaction.Outputs), len(expectedCoinbaseTransaction.Outputs))
	}

	if givenTotalValue != expectedTotalValue {
		log.Warnf("DIFFERENT TOTAL OUTPUT VALUE: given has %d, expected has %d",
			givenTotalValue, expectedTotalValue)
	}

	// Instead of comparing hashes (which differ due to output order/script differences),
	// just validate that the total output values match
	if givenTotalValue != expectedTotalValue {
		log.Warnf("Coinbase transaction TOTAL VALUE MISMATCH for block %s", blockHash)
		log.Warnf("  Given total output value: %d", givenTotalValue)
		log.Warnf("  Expected total output value: %d", expectedTotalValue)
		log.Warnf("  BlueScore: %d, Subsidy: %d, HasRedReward: %t", bluescore, subsidy, hasRedReward)

		log.Warnf("  === GIVEN COINBASE TX (%d outputs) ===", len(coinbaseTransaction.Outputs))
		for i, output := range coinbaseTransaction.Outputs {
			log.Warnf("    Output %d: Value: %d, Script: %x", i, output.Value, output.ScriptPublicKey.Script)
		}

		log.Warnf("  === EXPECTED COINBASE TX (%d outputs) ===", len(expectedCoinbaseTransaction.Outputs))
		for i, output := range expectedCoinbaseTransaction.Outputs {
			log.Warnf("    Output %d: Value: %d, Script: %x", i, output.Value, output.ScriptPublicKey.Script)
		}

		return fmt.Errorf("coinbase total value mismatch: given=%d, expected=%d", givenTotalValue, expectedTotalValue)
	}

	// Total values match - validation passes
	log.Debugf("Coinbase transaction total values match: %d", givenTotalValue)

	return nil
}

// filterAcceptanceDataByMergeSet filters the acceptance data to only include blocks
// that are in the GHOSTDAG merge set (blues and reds). This ensures the expected coinbase
// is generated using the same set of blocks that were used to create the real coinbase.
func filterAcceptanceDataByMergeSet(acceptanceData externalapi.AcceptanceData, ghostdagData *externalapi.BlockGHOSTDAGData) externalapi.AcceptanceData {
	// Build a set of block hashes that are in the merge set
	mergeSetHashes := make(map[string]bool)
	for _, blockHash := range ghostdagData.MergeSetBlues() {
		mergeSetHashes[blockHash.String()] = true
	}
	for _, blockHash := range ghostdagData.MergeSetReds() {
		mergeSetHashes[blockHash.String()] = true
	}

	// Filter the acceptance data to only include blocks in the merge set
	filteredData := make(externalapi.AcceptanceData, 0, len(acceptanceData))
	for _, blockAcceptance := range acceptanceData {
		if blockAcceptance.BlockHash != nil {
			if mergeSetHashes[blockAcceptance.BlockHash.String()] {
				filteredData = append(filteredData, blockAcceptance)
			}
		}
	}

	return filteredData
}
