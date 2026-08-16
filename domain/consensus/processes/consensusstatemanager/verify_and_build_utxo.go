package consensusstatemanager

import (
	"bytes"
	"sort"
	"sync"

	"github.com/HoosatNetwork/HTND/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/transactionhelper"

	"github.com/HoosatNetwork/HTND/domain/consensus/utils/consensushashing"

	"github.com/HoosatNetwork/HTND/domain/consensus/utils/merkle"

	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/ruleerrors"
	"github.com/HoosatNetwork/HTND/infrastructure/db/database"
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

	coinbaseTransaction := block.Transactions[0]
	err = csm.validateCoinbaseTransaction(stagingArea, block, blockHash, coinbaseTransaction, acceptanceData)
	if err != nil {
		return err
	}
	log.Debugf("Coinbase transaction validation passed for block %s", blockHash)

	log.Debugf("Validating transactions against past UTXO for block %s", blockHash)
	err = csm.validateBlockTransactionsAgainstPastUTXO(stagingArea, block, pastUTXODiff)
	if err != nil {
		return err
	}
	log.Debugf("Block transaction against past UTXO passed for %s", blockHash)
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

func (csm *consensusStateManager) validateCoinbaseTransaction(stagingArea *model.StagingArea, block *externalapi.DomainBlock,
	blockHash *externalapi.DomainHash, coinbaseTransaction *externalapi.DomainTransaction, acceptanceData externalapi.AcceptanceData,
) error {
	log.Tracef("validateCoinbaseTransaction start for block %s", blockHash)
	defer log.Tracef("validateCoinbaseTransaction end for block %s", blockHash)

	// Filter acceptance data to only include blocks in the merge set.
	// ExpectedCoinbaseTransactionInternal will retrieve its own GHOSTDAG data and
	// iterate over the merge set. We need to ensure it only processes blocks in the merge set.
	// However, since ExpectedCoinbaseTransactionInternal retrieves its own GHOSTDAG data,
	// we filter here using the same approach to ensure consistency.
	
	// Get GHOSTDAG data to determine the merge set for filtering
	var ghostdagData *externalapi.BlockGHOSTDAGData
	var err error
	ghostdagData, err = csm.ghostdagDataStore.Get(csm.databaseContext, stagingArea, blockHash, false)
	if database.IsNotFoundError(err) {
		ghostdagData, err = csm.ghostdagDataStore.Get(csm.databaseContext, stagingArea, blockHash, true)
		if err != nil {
			log.Warnf("Could not retrieve GHOSTDAG data for block %s, using unfiltered acceptance data as fallback", blockHash)
			ghostdagData = nil
		} else {
			log.Tracef("Retrieved GHOSTDAG data from trusted store for block %s", blockHash)
		}
	}
	if err != nil && !database.IsNotFoundError(err) {
		return err
	}

	// Filter acceptance data to only include blocks in the merge set
	var filteredAcceptanceData externalapi.AcceptanceData
	if ghostdagData != nil {
		filteredAcceptanceData = filterAcceptanceDataByMergeSet(acceptanceData, ghostdagData)
		log.Tracef("Filtered acceptance data from %d blocks to %d blocks (merge set only)", len(acceptanceData), len(filteredAcceptanceData))
		log.Tracef("Merge set: %d blues, %d reds", len(ghostdagData.MergeSetBlues()), len(ghostdagData.MergeSetReds()))
	} else {
		filteredAcceptanceData = acceptanceData
		log.Warnf("Using unfiltered acceptance data for block %s (GHOSTDAG data unavailable)", blockHash)
	}

	log.Tracef("Extracting coinbase data for coinbase transaction %s in block %s",
		consensushashing.TransactionID(coinbaseTransaction), blockHash)
	_, coinbaseData, _, err := csm.coinbaseManager.ExtractCoinbaseDataBlueScoreAndSubsidy(coinbaseTransaction)
	if err != nil {
		return err
	}

	log.Tracef("Calculating the expected coinbase transaction for the given coinbase data and block %s", blockHash)
	// Pass the header's blue score to ensure we use the same blue score as the block
	expectedCoinbaseTransaction, _, err := csm.coinbaseManager.ExpectedCoinbaseTransactionWithAcceptanceData(stagingArea, blockHash, coinbaseData, filteredAcceptanceData)
	if err != nil {
		return err
	}
	// Lets skip validation of the payload, because daascore or other data may change on expected payload.
	expectedCoinbaseTransaction.Payload = coinbaseTransaction.Payload

	coinbaseTransactionHash := consensushashing.TransactionHash(coinbaseTransaction)
	expectedCoinbaseTransactionHash := consensushashing.TransactionHash(expectedCoinbaseTransaction)
	log.Tracef("given coinbase hash: %s, expected coinbase hash: %s", coinbaseTransactionHash, expectedCoinbaseTransactionHash)
	
	// Debug: compare outputs in detail if hashes differ
	if !coinbaseTransactionHash.Equal(expectedCoinbaseTransactionHash) {
		if len(coinbaseTransaction.Outputs) == len(expectedCoinbaseTransaction.Outputs) {
			for i := range coinbaseTransaction.Outputs {
				actOut := coinbaseTransaction.Outputs[i]
				expOut := expectedCoinbaseTransaction.Outputs[i]
				if actOut.Value != expOut.Value {
					log.Infof("Output %d value differs: actual=%d, expected=%d", i, actOut.Value, expOut.Value)
				}
				if !bytes.Equal(actOut.ScriptPublicKey.Script, expOut.ScriptPublicKey.Script) {
					log.Infof("Output %d script differs: actual=%x, expected=%x", i, actOut.ScriptPublicKey.Script, expOut.ScriptPublicKey.Script)
				}
				if actOut.ScriptPublicKey.Version != expOut.ScriptPublicKey.Version {
					log.Infof("Output %d script version differs: actual=%d, expected=%d", i, actOut.ScriptPublicKey.Version, expOut.ScriptPublicKey.Version)
				}
			}
		} else {
			log.Infof("Output count differs: actual=%d, expected=%d", len(coinbaseTransaction.Outputs), len(expectedCoinbaseTransaction.Outputs))
		}
	}
	
	if !coinbaseTransactionHash.Equal(expectedCoinbaseTransactionHash) {
		log.Infof("Transaction hashes, coinbase %s != expected %s", coinbaseTransactionHash, expectedCoinbaseTransactionHash)

		// Log all transaction fields for comparison
		log.Infof("=== ACTUAL COINBASE ===")
		log.Infof("Version: %d", coinbaseTransaction.Version)
		log.Infof("LockTime: %d", coinbaseTransaction.LockTime)
		log.Infof("SubnetworkID: %s", coinbaseTransaction.SubnetworkID)
		log.Infof("Gas: %d", coinbaseTransaction.Gas)
		log.Infof("Fee: %d", coinbaseTransaction.Fee)
		log.Infof("Mass: %d", coinbaseTransaction.Mass)
		log.Infof("Payload length: %d, hex: %x", len(coinbaseTransaction.Payload), coinbaseTransaction.Payload)
		log.Infof("Inputs count: %d", len(coinbaseTransaction.Inputs))
		for i, input := range coinbaseTransaction.Inputs {
			log.Infof("  Input %d: Script(%s) Amount(%d)", i, string(input.SignatureScript), input.UTXOEntry.Amount())
		}
		log.Infof("Outputs count: %d", len(coinbaseTransaction.Outputs))
		for i, output := range coinbaseTransaction.Outputs {
			log.Infof("  Output %d: Script(%s) Value(%d)", i, output.ScriptPublicKey.String(), output.Value)
		}

		log.Infof("=== EXPECTED COINBASE ===")
		log.Infof("Version: %d", expectedCoinbaseTransaction.Version)
		log.Infof("LockTime: %d", expectedCoinbaseTransaction.LockTime)
		log.Infof("SubnetworkID: %s", expectedCoinbaseTransaction.SubnetworkID)
		log.Infof("Gas: %d", expectedCoinbaseTransaction.Gas)
		log.Infof("Fee: %d", expectedCoinbaseTransaction.Fee)
		log.Infof("Mass: %d", expectedCoinbaseTransaction.Mass)
		log.Infof("Payload length: %d, hex: %x", len(expectedCoinbaseTransaction.Payload), expectedCoinbaseTransaction.Payload)
		log.Infof("Inputs count: %d", len(expectedCoinbaseTransaction.Inputs))
		for i, input := range expectedCoinbaseTransaction.Inputs {
			log.Infof("  Input %d: Script(%s) Amount(%d)", i, string(input.SignatureScript), input.UTXOEntry.Amount())
		}
		log.Infof("Outputs count: %d", len(expectedCoinbaseTransaction.Outputs))
		for i, output := range expectedCoinbaseTransaction.Outputs {
			log.Infof("  Output %d: Script(%s) Value(%d)", i, output.ScriptPublicKey.String(), output.Value)
		}

		// Identify the specific difference
		if coinbaseTransaction.Version != expectedCoinbaseTransaction.Version {
			log.Infof("DIFFERENCE: Version (actual=%d, expected=%d)", coinbaseTransaction.Version, expectedCoinbaseTransaction.Version)
		}
		if coinbaseTransaction.LockTime != expectedCoinbaseTransaction.LockTime {
			log.Infof("DIFFERENCE: LockTime (actual=%d, expected=%d)", coinbaseTransaction.LockTime, expectedCoinbaseTransaction.LockTime)
		}
		if !coinbaseTransaction.SubnetworkID.Equal(&expectedCoinbaseTransaction.SubnetworkID) {
			log.Infof("DIFFERENCE: SubnetworkID (actual=%s, expected=%s)", coinbaseTransaction.SubnetworkID, expectedCoinbaseTransaction.SubnetworkID)
		}
		if coinbaseTransaction.Gas != expectedCoinbaseTransaction.Gas {
			log.Infof("DIFFERENCE: Gas (actual=%d, expected=%d)", coinbaseTransaction.Gas, expectedCoinbaseTransaction.Gas)
		}
		if coinbaseTransaction.Fee != expectedCoinbaseTransaction.Fee {
			log.Infof("DIFFERENCE: Fee (actual=%d, expected=%d)", coinbaseTransaction.Fee, expectedCoinbaseTransaction.Fee)
		}
		if coinbaseTransaction.Mass != expectedCoinbaseTransaction.Mass {
			log.Infof("DIFFERENCE: Mass (actual=%d, expected=%d)", coinbaseTransaction.Mass, expectedCoinbaseTransaction.Mass)
		}
		if !bytes.Equal(coinbaseTransaction.Payload, expectedCoinbaseTransaction.Payload) {
			log.Infof("DIFFERENCE: Payload (actual=%x, expected=%x)", coinbaseTransaction.Payload, expectedCoinbaseTransaction.Payload)
		}
		if len(coinbaseTransaction.Inputs) != len(expectedCoinbaseTransaction.Inputs) {
			log.Infof("DIFFERENCE: Inputs count (actual=%d, expected=%d)", len(coinbaseTransaction.Inputs), len(expectedCoinbaseTransaction.Inputs))
		}
		if len(coinbaseTransaction.Outputs) != len(expectedCoinbaseTransaction.Outputs) {
			log.Infof("DIFFERENCE: Outputs count (actual=%d, expected=%d)", len(coinbaseTransaction.Outputs), len(expectedCoinbaseTransaction.Outputs))
			// Log merge set info to help debug filtering issues
			if ghostdagData != nil {
				log.Infof("MERGE SET INFO: %d blues, %d reds in merge set", len(ghostdagData.MergeSetBlues()), len(ghostdagData.MergeSetReds()))
				log.Infof("FILTERED ACCEPTANCE: %d blocks in filtered acceptance data", len(filteredAcceptanceData))
			}
		}

		return errors.Wrap(ruleerrors.ErrBadCoinbaseTransaction, "coinbase transaction is not built as expected")
	}

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
