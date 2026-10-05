package consensusstatemanager

import (
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/ruleerrors"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/consensushashing"
)

// PopulateTransactionWithUTXOEntries populates the transaction UTXO entries with data from the virtual's UTXO set.
func (csm *consensusStateManager) PopulateTransactionWithUTXOEntries(
	stagingArea *model.StagingArea, transaction *externalapi.DomainTransaction,
) error {
	return csm.populateTransactionWithUTXOEntriesFromVirtualOrDiff(stagingArea, transaction, nil, nil)
}

// populateTransactionWithUTXOEntriesFromVirtualOrDiff populates the transaction UTXO entries with data
// from the virtual's UTXO set combined with the provided utxoDiff.
// If utxoDiff == nil UTXO entries are taken from the virtual's UTXO set only.
//
// spentInPass holds outpoints an earlier transaction in this same merge-set pass has already spent.
// Those are double spends. A coin that is only in utxoDiff.ToRemove is not: that set starts as the
// diff from virtual to the UTXO set being validated, so it contains every coin virtual holds and the
// set does not, which follows block arrival order. Such a coin is missing, and it must not be filled
// from virtual's table. Callers that are not walking a merge set pass nil.
func (csm *consensusStateManager) populateTransactionWithUTXOEntriesFromVirtualOrDiff(stagingArea *model.StagingArea,
	transaction *externalapi.DomainTransaction, utxoDiff externalapi.UTXODiff,
	spentInPass map[externalapi.DomainOutpoint]struct{},
) error {
	// fast exit for coinbase
	if len(transaction.Inputs) == 0 {
		return nil
	}

	transactionID := consensushashing.TransactionID(transaction)
	log.Tracef("populateTransactionWithUTXOEntriesFromVirtualOrDiff start for transaction %s", transactionID)
	defer log.Tracef("populateTransactionWithUTXOEntriesFromVirtualOrDiff end for transaction %s", transactionID)

	var missingOutpoints []*externalapi.DomainOutpoint
	// Inputs spent by an earlier transaction in this same merge-set pass. That is a double spend.
	// An input the set being validated simply does not hold is not, even when virtual's table still
	// has the coin: whether virtual has it follows which blocks have been resolved, and two nodes
	// with the same DAG resolve them in different orders. Kept apart so a node whose set is known
	// to be incomplete can tolerate the second without ever tolerating the first.
	var spentOutpoints []*externalapi.DomainOutpoint
	for _, transactionInput := range transaction.Inputs {
		// skip all inputs that have a pre-filled utxo entry
		if transactionInput.UTXOEntry != nil {
			log.Tracef("Skipping outpoint %s:%d because it is already populated",
				transactionInput.PreviousOutpoint.TransactionID, transactionInput.PreviousOutpoint.Index)
			continue
		}

		if _, spent := spentInPass[transactionInput.PreviousOutpoint]; spent {
			log.Tracef("Outpoint %s:%d was spent by an earlier transaction in this merge set - double spend",
				transactionInput.PreviousOutpoint.TransactionID, transactionInput.PreviousOutpoint.Index)
			missingOutpoints = append(missingOutpoints, &transactionInput.PreviousOutpoint)
			spentOutpoints = append(spentOutpoints, &transactionInput.PreviousOutpoint)
			continue
		}

		// check if utxoDiff says anything about the input's outpoint
		if utxoDiff != nil {
			if utxoEntry, ok := utxoDiff.ToAdd().Get(&transactionInput.PreviousOutpoint); ok {
				log.Tracef("Populating outpoint %s:%d from the given utxoDiff",
					transactionInput.PreviousOutpoint.TransactionID, transactionInput.PreviousOutpoint.Index)
				transactionInput.UTXOEntry = utxoEntry
				continue
			}

			if utxoDiff.ToRemove().Contains(&transactionInput.PreviousOutpoint) {
				// Virtual holds this coin and the set being validated does not. Taking the virtual
				// entry would spend a coin that set does not contain, and calling it a double spend
				// makes the verdict depend on virtual's position.
				log.Tracef("Outpoint %s:%d is absent from the UTXO set being validated",
					transactionInput.PreviousOutpoint.TransactionID, transactionInput.PreviousOutpoint.Index)
				missingOutpoints = append(missingOutpoints, &transactionInput.PreviousOutpoint)
				continue
			}
		}

		// Check for the input's outpoint in virtual's UTXO set. One lookup, which consults the UTXO
		// cache: asking HasUTXOByOutpoint first read the database for every input, even a cached one.
		utxoEntry, hasUTXOEntry, err := csm.consensusStateStore.LookupUTXOByOutpoint(
			csm.databaseContext, stagingArea, &transactionInput.PreviousOutpoint)
		if err != nil {
			return err
		}
		if !hasUTXOEntry {
			log.Tracef("Outpoint %s:%d is missing in the database",
				transactionInput.PreviousOutpoint.TransactionID, transactionInput.PreviousOutpoint.Index)
			missingOutpoints = append(missingOutpoints, &transactionInput.PreviousOutpoint)
			continue
		}

		log.Tracef("Populating outpoint %s:%d from the database",
			transactionInput.PreviousOutpoint.TransactionID, transactionInput.PreviousOutpoint.Index)
		transactionInput.UTXOEntry = utxoEntry
	}

	if len(missingOutpoints) > 0 {
		return ruleerrors.NewErrMissingOrSpentTxOut(missingOutpoints, spentOutpoints)
	}

	return nil
}

func (csm *consensusStateManager) populateTransactionWithUTXOEntriesFromUTXOSet(
	pruningPoint *externalapi.DomainBlock, iterator externalapi.ReadOnlyUTXOSetIterator,
) error {
	// Collect the required outpoints from the block
	outpointsForPopulation := make(map[externalapi.DomainOutpoint]any)
	for _, transaction := range pruningPoint.Transactions {
		for _, input := range transaction.Inputs {
			outpointsForPopulation[input.PreviousOutpoint] = struct{}{}
		}
	}

	// Collect the UTXO entries from the iterator
	outpointsToUTXOEntries := make(map[externalapi.DomainOutpoint]externalapi.UTXOEntry, len(outpointsForPopulation))
	for ok := iterator.First(); ok; ok = iterator.Next() {
		outpoint, utxoEntry, err := iterator.Get()
		if err != nil {
			return err
		}
		outpointValue := *outpoint
		if _, ok := outpointsForPopulation[outpointValue]; ok {
			outpointsToUTXOEntries[outpointValue] = utxoEntry
		}
		if len(outpointsForPopulation) == len(outpointsToUTXOEntries) {
			break
		}
	}

	// Populate the block with the collected UTXO entries
	var missingOutpoints []*externalapi.DomainOutpoint
	for _, transaction := range pruningPoint.Transactions {
		for _, input := range transaction.Inputs {
			utxoEntry, ok := outpointsToUTXOEntries[input.PreviousOutpoint]
			if !ok {
				missingOutpoints = append(missingOutpoints, &input.PreviousOutpoint)
				continue
			}
			input.UTXOEntry = utxoEntry
		}
	}

	if len(missingOutpoints) > 0 {
		return ruleerrors.NewErrMissingTxOut(missingOutpoints)
	}
	return nil
}
