package consensusstatemanager

import (
	"sync"

	"github.com/Hoosat-Oy/HTND/domain/consensus/model"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/ruleerrors"
)

// PopulateTransactionWithUTXOEntries populates the transaction UTXO entries with data from the virtual's UTXO set.
func (csm *consensusStateManager) PopulateTransactionWithUTXOEntries(
	stagingArea *model.StagingArea, transaction *externalapi.DomainTransaction) error {
	return csm.populateTransactionWithUTXOEntriesFromVirtualOrDiff(stagingArea, transaction, nil)
}

// populateTransactionWithUTXOEntriesFromVirtualOrDiff populates the transaction UTXO entries with data
// from the virtual's UTXO set combined with the provided utxoDiff.
// If utxoDiff == nil UTXO entries are taken from the virtual's UTXO set only
func (csm *consensusStateManager) populateTransactionWithUTXOEntriesFromVirtualOrDiff(stagingArea *model.StagingArea,
	transaction *externalapi.DomainTransaction, utxoDiff externalapi.UTXODiff) error {

	var (
		missingOutpoints []*externalapi.DomainOutpoint
		missingMu        sync.Mutex // Protects missingOutpoints
		outpointsMu      sync.Mutex // Protects transaction.Inputs
		errMu            sync.Mutex // Protects error collection
		firstErr         error      // Stores the first error encountered
		wg               sync.WaitGroup
	)

	// Process each input in a separate goroutine
	for i := 0; i < len(transaction.Inputs); i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()

			// Skip if error already occurred
			errMu.Lock()
			if firstErr != nil {
				errMu.Unlock()
				return
			}
			errMu.Unlock()

			// Skip inputs with pre-filled UTXO entries
			if transaction.Inputs[index].UTXOEntry != nil {
				return
			}

			// Check utxoDiff if provided
			if utxoDiff != nil {
				if utxoEntry, ok := utxoDiff.ToAdd().Get(&transaction.Inputs[index].PreviousOutpoint); ok {
					transaction.Inputs[index].UTXOEntry = utxoEntry
					return
				}

				if utxoDiff.ToRemove().Contains(&transaction.Inputs[index].PreviousOutpoint) {
					missingMu.Lock()
					missingOutpoints = append(missingOutpoints, &transaction.Inputs[index].PreviousOutpoint)
					missingMu.Unlock()
					return
				}
			}

			// Check virtual's UTXO set
			outpointsMu.Lock()
			utxoEntry, hasUTXOEntry, err := csm.consensusStateStore.UTXOByOutpoint(csm.databaseContext, stagingArea, &transaction.Inputs[index].PreviousOutpoint)
			outpointsMu.Unlock()
			if !hasUTXOEntry {
				missingMu.Lock()
				missingOutpoints = append(missingOutpoints, &transaction.Inputs[index].PreviousOutpoint)
				missingMu.Unlock()
				return
			}
			if err != nil {
				errMu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				errMu.Unlock()
				return
			}

			transaction.Inputs[index].UTXOEntry = utxoEntry
		}(i)
	}

	// Wait for all goroutines to complete
	wg.Wait()

	// Check for errors
	if firstErr != nil {
		return firstErr
	}

	// Check for missing outpoints
	if len(missingOutpoints) > 0 {
		return ruleerrors.NewErrMissingTxOut(missingOutpoints)
	}

	return nil
}

func (csm *consensusStateManager) populateTransactionWithUTXOEntriesFromUTXOSet(
	pruningPoint *externalapi.DomainBlock, iterator externalapi.ReadOnlyUTXOSetIterator) error {

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
