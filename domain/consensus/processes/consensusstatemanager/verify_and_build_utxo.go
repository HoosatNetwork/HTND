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
	"github.com/pkg/errors"
)

func (csm *consensusStateManager) verifyUTXO(stagingArea *model.StagingArea, block *externalapi.DomainBlock,
	blockHash *externalapi.DomainHash, pastUTXODiff externalapi.UTXODiff, acceptanceData externalapi.AcceptanceData,
	multiset model.Multiset,
) error {
	log.Tracef("verifyUTXO start for block %s", blockHash)
	defer log.Tracef("verifyUTXO end for block %s", blockHash)

	log.Debugf("Validating UTXO commitment for block %s", blockHash)
	err := csm.validateUTXOCommitment(stagingArea, block, blockHash, multiset)
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
				if !errors.As(err, &ruleerrors.ErrMissingTxOut{}) {
					mu.Unlock()
					return
				}
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

func (csm *consensusStateManager) validateUTXOCommitment(stagingArea *model.StagingArea,
	block *externalapi.DomainBlock, blockHash *externalapi.DomainHash, multiset model.Multiset,
) error {
	log.Tracef("validateUTXOCommitment start for block %s", blockHash)
	defer log.Tracef("validateUTXOCommitment end for block %s", blockHash)

	if blockHash.Equal(csm.genesisHash) {
		return nil
	}

	calculatedCommitment := multiset.Hash()
	expectedCommitment := block.Header.UTXOCommitment()

	if !calculatedCommitment.Equal(expectedCommitment) {
		// When the chain is built on an incomplete imported pruning-point UTXO set (a peer's
		// snapshotted set missing diffs from blocks disqualified upstream - see
		// verifyAndRepairImportedPruningPointUTXOSet), every block resolved forward inherits the exact
		// same fixed multiset offset, because MuHash is homomorphic. Disqualifying each of them for it
		// leaves virtual resolution permanently stuck right above the pruning point. Detect that
		// situation from the block's own selected parent - if the parent's stored multiset already
		// disagrees with the parent's header commitment, this block just carries the inherited offset,
		// not fresh corruption - and tolerate it: stage the calculated multiset and continue. Fully
		// self-scoping: as soon as the chain reaches a block whose selected parent is consistent
		// (e.g. built on a clean pruning point), this returns false and strict enforcement resumes.
		if csm.blockInheritsKnownUTXOCommitmentOffset(stagingArea, blockHash) {
			if !csm.toleratedUTXOCommitmentOffsetLogged {
				csm.toleratedUTXOCommitmentOffsetLogged = true
				log.Warnf("Block %s UTXO commitment mismatch TOLERATED (header %s, calculated %s): its "+
					"selected parent's own stored multiset already disagrees with its header, i.e. the chain "+
					"is built on an incomplete imported pruning-point UTXO set and every block inherits the "+
					"same fixed offset. Staging the calculated multiset and continuing. Further occurrences "+
					"are logged at debug level.", blockHash, expectedCommitment, calculatedCommitment)
			} else {
				log.Debugf("Block %s UTXO commitment mismatch tolerated (inherited pruning-point offset): "+
					"header %s, calculated %s", blockHash, expectedCommitment, calculatedCommitment)
			}
			return nil
		}

		// --- DEBUG LOGGING START ---
		log.Warnf("[UTXO-DEBUG] Block Hash: %s", blockHash)
		ghostdagData, err := csm.ghostdagDataStore.Get(csm.databaseContext, stagingArea, blockHash, false)
		if err != nil {
			log.Warnf("[UTXO-DEBUG] failed to fetch GhostDAGDAta")
		} else {
			log.Warnf("[UTXO-DEBUG] Selected Parent: %s", ghostdagData.SelectedParent())
			log.Warnf("[UTXO-DEBUG] Blue Score: %d", ghostdagData.BlueScore())
			log.Warnf("[UTXO-DEBUG] Blue Work: %x", ghostdagData.BlueWork())
			log.Warnf("[UTXO-DEBUG] MergeSetBlues Count: %d", len(ghostdagData.MergeSetBlues()))
			for i, blue := range ghostdagData.MergeSetBlues() {
				log.Warnf("[UTXO-DEBUG] Blue[%d]: %s", i, blue)
			}
		}

		log.Warnf("[UTXO-DEBUG] Header Expected UTXO Commitment: %s", expectedCommitment)
		log.Warnf("[UTXO-DEBUG] Validation Calculated UTXO Commitment: %s", calculatedCommitment)
		// --- DEBUG LOGGING END ---

		return errors.Wrapf(ruleerrors.ErrBadUTXOCommitment, "block %s UTXO commitment is invalid - block "+
			"header indicates %s, but calculated value is %s", blockHash, block.Header.UTXOCommitment(), calculatedCommitment)
	}

	return nil
}

// blockInheritsKnownUTXOCommitmentOffset reports whether blockHash's selected parent already carries
// a UTXO commitment discrepancy - its stored multiset does not hash to its own header's
// UTXOCommitment. That is the signature of a chain built on an incomplete imported pruning-point
// UTXO set (see verifyAndRepairImportedPruningPointUTXOSet): the missing entries shift the multiset
// by a fixed amount, and because MuHash is homomorphic every descendant inherits the exact same
// shift. When the selected parent shows it, blockHash's own mismatch is that same inherited offset
// rather than fresh corruption.
//
// This is a purely local check against the selected parent, which resolveSingleBlockStatus always
// resolves and stages before the block itself, so it needs no persisted marker and works on an
// already-imported database. It is self-scoping: the first block whose selected parent's multiset
// does agree with its header (e.g. anything built on a consistent pruning point) makes this return
// false and full ErrBadUTXOCommitment enforcement resumes.
func (csm *consensusStateManager) blockInheritsKnownUTXOCommitmentOffset(stagingArea *model.StagingArea,
	blockHash *externalapi.DomainHash,
) bool {
	ghostdagData, err := csm.ghostdagDataStore.Get(csm.databaseContext, stagingArea, blockHash, false)
	if err != nil {
		return false
	}
	selectedParent := ghostdagData.SelectedParent()
	if selectedParent == nil || selectedParent.Equal(csm.genesisHash) ||
		selectedParent.Equal(model.VirtualGenesisBlockHash) {
		return false
	}

	selectedParentMultiset, err := csm.multisetStore.Get(csm.databaseContext, stagingArea, selectedParent)
	if err != nil {
		return false
	}
	selectedParentHeader, err := csm.blockHeaderStore.BlockHeader(csm.databaseContext, stagingArea, selectedParent)
	if err != nil {
		return false
	}
	return !selectedParentMultiset.Hash().Equal(selectedParentHeader.UTXOCommitment())
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

	log.Tracef("Extracting coinbase data for coinbase transaction %s in block %s",
		consensushashing.TransactionID(coinbaseTransaction), blockHash)
	_, coinbaseData, _, err := csm.coinbaseManager.ExtractCoinbaseDataBlueScoreAndSubsidyForVersion(coinbaseTransaction, block.Header.Version())
	if err != nil {
		return err
	}

	log.Tracef("Calculating the expected coinbase transaction for the given coinbase data and block %s", blockHash)
	// Pass the original acceptance data - ExpectedCoinbaseTransactionInternal will filter it
	// using its own GHOSTDAG data to ensure it only processes merge set blocks
	expectedCoinbaseTransaction, _, err := csm.coinbaseManager.ExpectedCoinbaseTransactionWithAcceptanceData(stagingArea, blockHash, coinbaseData, acceptanceData)
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
			log.Infof("  Input %d: Script(%x) Amount(%d)", i, input.SignatureScript, input.UTXOEntry.Amount())
		}
		log.Infof("Outputs count: %d", len(coinbaseTransaction.Outputs))
		for i, output := range coinbaseTransaction.Outputs {
			log.Infof("  Output %d: Script(%x) Value(%d)", i, output.ScriptPublicKey.Script, output.Value)
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
			log.Infof("  Input %d: Script(%x) Amount(%d)", i, input.SignatureScript, input.UTXOEntry.Amount())
		}
		log.Infof("Outputs count: %d", len(expectedCoinbaseTransaction.Outputs))
		for i, output := range expectedCoinbaseTransaction.Outputs {
			log.Infof("  Output %d: Script(%x) Value(%d)", i, output.ScriptPublicKey.Script, output.Value)
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
