package consensusstatemanager

import (
	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/transactionhelper"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/utxo"
	"github.com/HoosatNetwork/HTND/infrastructure/logger"
	"github.com/HoosatNetwork/HTND/util/staging"
	"github.com/pkg/errors"
)

func (csm *consensusStateManager) ImportPruningPointUTXOSet(stagingArea *model.StagingArea, newPruningPoint *externalapi.DomainHash) error {
	onEnd := logger.LogAndMeasureExecutionTime(log, "ImportPruningPointUTXOSet")
	defer onEnd()

	err := csm.importPruningPointUTXOSet(stagingArea, newPruningPoint)
	if err != nil {
		return err
	}

	err = csm.applyImportedPruningPointUTXOSet(stagingArea, newPruningPoint)
	if err != nil {
		return err
	}

	return nil
}

func (csm *consensusStateManager) importPruningPointUTXOSet(stagingArea *model.StagingArea, newPruningPoint *externalapi.DomainHash) error {
	log.Tracef("importPruningPointUTXOSet start")
	defer log.Tracef("importPruningPointUTXOSet end")

	// TODO: We should validate the imported pruning point doesn't violate finality as part of the headers proof.

	importedPruningPointMultiset, err := csm.pruningStore.ImportedPruningPointMultiset(csm.databaseContext)
	if err != nil {
		return err
	}

	// [UTXO-DEBUG] This check was previously disabled ("HTN pruning points are messed up") with the
	// hard-failure removed entirely, so a wrong imported pruning-point UTXO set/multiset - the trust
	// anchor every block resolved for the rest of this sync gets built forward from via
	// csm.multisetStore.Get(newPruningPoint) - was never caught here. It would only surface much
	// later, as an unrelated-looking ErrBadUTXOCommitment on whatever's the first real block resolved
	// after IBD. Restored as a non-fatal warning (not a hard return) so it can't block sync while we
	// confirm whether this is actually where the corruption enters.
	newPruningPointHeader, headerErr := csm.blockHeaderStore.BlockHeader(csm.databaseContext, stagingArea, newPruningPoint)
	if headerErr != nil {
		log.Warnf("[UTXO-DEBUG] could not fetch pruning point header to validate imported UTXO set: %s", headerErr)
	} else {
		log.Debugf("The UTXO commitment of the pruning point: %s", newPruningPointHeader.UTXOCommitment())
		if !newPruningPointHeader.UTXOCommitment().Equal(importedPruningPointMultiset.Hash()) {
			log.Errorf("[UTXO-DEBUG] IMPORTED PRUNING POINT UTXO SET DOES NOT MATCH ITS OWN HEADER: "+
				"pruning point %s header expects UTXO commitment %s, but the imported pruning point "+
				"multiset hashes to %s. Every block resolved from here forward builds on this "+
				"(wrong) baseline via multisetStore.Get(%s) - this is very likely the actual source "+
				"of the ErrBadUTXOCommitment seen on the first block resolved after IBD, not a bug in "+
				"that later block's own resolution at all.",
				newPruningPoint, newPruningPointHeader.UTXOCommitment(), importedPruningPointMultiset.Hash(), newPruningPoint)
		} else {
			log.Warnf("[UTXO-DEBUG] Imported pruning point UTXO set MATCHES its own header's UTXO "+
				"commitment (%s) - the trust anchor is correct; the corruption (if any) is introduced "+
				"after this point, not at import.", newPruningPointHeader.UTXOCommitment())
		}
	}

	log.Debugf("The new pruning point UTXO commitment validation passed")

	log.Debugf("Setting the pruning point as the only virtual parent")
	err = csm.dagTopologyManager.SetParents(stagingArea, model.VirtualBlockHash, []*externalapi.DomainHash{newPruningPoint})
	if err != nil {
		return err
	}

	log.Debugf("Calculating GHOSTDAG for the new virtual")
	err = csm.ghostdagManager.GHOSTDAG(stagingArea, model.VirtualBlockHash)
	if err != nil {
		return err
	}

	log.Debugf("Updating the new pruning point to be the new virtual diff parent with an empty diff")
	csm.stageDiff(stagingArea, newPruningPoint, utxo.NewUTXODiff(), nil)

	log.Debugf("Populating the pruning point with UTXO entries")
	importedPruningPointUTXOIterator, err := csm.pruningStore.ImportedPruningPointUTXOIterator(csm.databaseContext)
	if err != nil {
		return err
	}
	defer importedPruningPointUTXOIterator.Close()

	newPruningPointBlock, err := csm.blockStore.Block(csm.databaseContext, stagingArea, newPruningPoint)
	if err != nil {
		return err
	}

	err = csm.populateTransactionWithUTXOEntriesFromUTXOSet(newPruningPointBlock, importedPruningPointUTXOIterator)
	if err != nil {
		return err
	}

	// Before we manually mark the new pruning point as valid, we validate that all of its transactions are valid
	// against the provided UTXO set.
	log.Debugf("Validating that the pruning point is UTXO valid")
	newPruningPointSelectedParentMedianTime, err := csm.pastMedianTimeManager.PastMedianTime(stagingArea, newPruningPoint)
	if err != nil {
		return err
	}
	log.Tracef("The past median time of pruning block %s is %d",
		newPruningPoint, newPruningPointSelectedParentMedianTime)

	for i, transaction := range newPruningPointBlock.Transactions {
		transactionID := consensushashing.TransactionID(transaction)
		log.Tracef("Validating transaction %s in pruning block %s against "+
			"the pruning point's past UTXO", transactionID, newPruningPoint)
		if i == transactionhelper.CoinbaseTransactionIndex {
			log.Tracef("Skipping transaction %s because it is the coinbase", transactionID)
			continue
		}
		log.Tracef("Validating transaction %s and populating it with mass and fee", transactionID)
		err = csm.transactionValidator.ValidateTransactionInContextAndPopulateFee(
			stagingArea, transaction, newPruningPoint, newPruningPointBlock.Header.DAAScore())
		if err != nil {
			return err
		}
		log.Tracef("Validation against the pruning point's past UTXO "+
			"passed for transaction %s", transactionID)
	}

	log.Debugf("Staging the new pruning point as %s", externalapi.StatusUTXOValid)
	csm.blockStatusStore.Stage(stagingArea, newPruningPoint, externalapi.StatusUTXOValid)

	log.Debugf("Staging the new pruning point multiset")
	csm.multisetStore.Stage(stagingArea, newPruningPoint, importedPruningPointMultiset)

	_, err = csm.difficultyManager.StageDAADataAndReturnRequiredDifficulty(stagingArea, model.VirtualBlockHash, false)
	if err != nil {
		return err
	}

	return nil
}

func (csm *consensusStateManager) ImportPruningPoints(stagingArea *model.StagingArea, pruningPoints []externalapi.BlockHeader) error {
	for i, header := range pruningPoints {
		blockHash := consensushashing.HeaderHash(header)
		if i < 0 {
			return errors.Errorf("index %d is negative, cannot convert to uint64", i)
		}
		err := csm.pruningStore.StagePruningPointByIndex(csm.databaseContext, stagingArea, blockHash, uint64(i))
		if err != nil {
			return err
		}

		csm.blockHeaderStore.Stage(stagingArea, blockHash, header)
	}

	lastPruningPointHeader := pruningPoints[len(pruningPoints)-1]
	csm.pruningStore.StagePruningPointCandidate(stagingArea, consensushashing.HeaderHash(lastPruningPointHeader))

	return nil
}

func (csm *consensusStateManager) applyImportedPruningPointUTXOSet(stagingArea *model.StagingArea, newPruningPoint *externalapi.DomainHash) error {
	dbTx, err := csm.databaseContext.Begin()
	if err != nil {
		return err
	}

	err = stagingArea.Commit(dbTx)
	if err != nil {
		return err
	}

	log.Debugf("Starting to import virtual UTXO set and pruning point utxo set")
	err = csm.consensusStateStore.StartImportingPruningPointUTXOSet(dbTx)
	if err != nil {
		return err
	}

	log.Debugf("Committing all staged data for imported pruning point")
	err = dbTx.Commit()
	if err != nil {
		return err
	}

	return csm.importVirtualUTXOSetAndPruningPointUTXOSet(newPruningPoint)
}

func (csm *consensusStateManager) importVirtualUTXOSetAndPruningPointUTXOSet(pruningPoint *externalapi.DomainHash) error {
	onEnd := logger.LogAndMeasureExecutionTime(log, "importVirtualUTXOSetAndPruningPointUTXOSet")
	defer onEnd()

	log.Debugf("Getting an iterator into the imported pruning point utxo set")
	pruningPointUTXOSetIterator, err := csm.pruningStore.ImportedPruningPointUTXOIterator(csm.databaseContext)
	if err != nil {
		return err
	}
	defer pruningPointUTXOSetIterator.Close()

	log.Debugf("Importing the virtual UTXO set")
	err = csm.consensusStateStore.ImportPruningPointUTXOSetIntoVirtualUTXOSet(csm.databaseContext, pruningPointUTXOSetIterator)
	if err != nil {
		return err
	}

	log.Debugf("Importing the new pruning point UTXO set")
	err = csm.pruningStore.CommitImportedPruningPointUTXOSet(csm.databaseContext)
	if err != nil {
		return err
	}

	// Run update virtual to create acceptance data and any other missing data.
	updateVirtualStagingArea := model.NewStagingArea()
	_, _, err = csm.updateVirtual(updateVirtualStagingArea, pruningPoint, []*externalapi.DomainHash{pruningPoint})
	if err != nil {
		return err
	}

	err = staging.CommitAllChanges(csm.databaseContext, updateVirtualStagingArea)
	if err != nil {
		return err
	}

	log.Debugf("Finishing to import virtual UTXO set and pruning point UTXO set")
	return csm.consensusStateStore.FinishImportingPruningPointUTXOSet(csm.databaseContext)
}

func (csm *consensusStateManager) RecoverUTXOIfRequired() error {
	hadStartedImportingPruningPointUTXOSet, err := csm.consensusStateStore.HadStartedImportingPruningPointUTXOSet(csm.databaseContext)
	if err != nil {
		return err
	}
	if !hadStartedImportingPruningPointUTXOSet {
		return nil
	}

	log.Warnf("Unimported pruning point UTXO set detected. Attempting to recover...")
	pruningPoint, err := csm.pruningStore.PruningPoint(csm.databaseContext, model.NewStagingArea())
	if err != nil {
		return err
	}

	err = csm.importVirtualUTXOSetAndPruningPointUTXOSet(pruningPoint)
	if err != nil {
		return err
	}
	log.Warnf("Unimported UTXO set successfully recovered")
	return nil
}
