package consensusstatemanager

import (
	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/ruleerrors"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/multiset"
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

	// Verify the peer-supplied pruning point UTXO set against the pruning point's own header
	// commitment and repair it before anything else builds on it. importedPruningPointMultiset is
	// the trust anchor csm.multisetStore.Get(newPruningPoint) hands to every block resolved for the
	// rest of this sync; if it disagrees with the stored UTXO entries the node's own state is
	// inconsistent and later blocks fail with unrelated-looking ErrBadUTXOCommitment.
	importedPruningPointMultiset, err = csm.verifyAndRepairImportedPruningPointUTXOSet(
		stagingArea, newPruningPoint, importedPruningPointMultiset)
	if err != nil {
		return err
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

// verifyAndRepairImportedPruningPointUTXOSet checks the freshly-downloaded pruning point UTXO set
// against newPruningPoint's own header UTXO commitment and repairs the node's trust anchor so that
// the pruning point's stored multiset always equals a fresh hash of the UTXO entries actually
// persisted in the bucket - the set CommitImportedPruningPointUTXOSet promotes to the served
// pruning point UTXO set and that every later block builds forward from via
// csm.multisetStore.Get(newPruningPoint).
//
// accumulatedMultiset is built incrementally in pruningManager.AppendImportedPruningPointUTXOs as
// UTXO-set chunks stream in: every (outpoint, entry) pair in every chunk is added to the multiset
// unconditionally, while the entries themselves are written to the imported-pruning-point-utxos
// bucket keyed by outpoint. A MuHash Add is not idempotent, so any pair delivered more than once
// (a resent chunk, a mid-stream reconnect, leftover bytes from an earlier aborted import) inflates
// the accumulated multiset while the outpoint-keyed bucket silently overwrites the duplicate.
//
// Outcomes:
//   - accumulated multiset matches the header: nothing to do, use it.
//   - it doesn't, but a fresh multiset over the deduplicated bucket does: the accumulation
//     double-counted; use the recomputed multiset - the set itself was fine.
//   - neither matches: the served set is genuinely incomplete (the known HTN condition where
//     blocks disqualified upstream leave their UTXO diffs out of the snapshotted pruning-point
//     set). This node can't reconstruct the missing entries, and every peer has the same gap, so
//     failing the import just prevents sync entirely. Proceed on the recomputed multiset anyway,
//     loudly, so at least the bucket and the per-block multiset are mutually consistent; blocks
//     resolved forward may still mismatch their own header commitments until the upstream
//     disqualifications are fixed.
//
// A truly empty bucket (a truncated transfer, not a "messed up" pruning point) is still a hard
// ErrBadPruningPointUTXOSet so the IBD flow retries another peer.
func (csm *consensusStateManager) verifyAndRepairImportedPruningPointUTXOSet(stagingArea *model.StagingArea,
	newPruningPoint *externalapi.DomainHash, accumulatedMultiset model.Multiset,
) (model.Multiset, error) {
	header, err := csm.blockHeaderStore.BlockHeader(csm.databaseContext, stagingArea, newPruningPoint)
	if err != nil {
		// Without the header there is nothing to check against; proceed with whatever the peer supplied.
		log.Warnf("Could not fetch pruning point %s header to validate the imported UTXO set (%s) - "+
			"proceeding with the accumulated multiset", newPruningPoint, err)
		return accumulatedMultiset, nil
	}
	expectedCommitment := header.UTXOCommitment()

	if expectedCommitment.Equal(accumulatedMultiset.Hash()) {
		log.Infof("Imported pruning point %s UTXO set matches its own header commitment %s",
			newPruningPoint, expectedCommitment)
		return accumulatedMultiset, nil
	}

	log.Warnf("Imported pruning point %s UTXO set does not match its own header: header expects "+
		"commitment %s, accumulated multiset hashes to %s. Recomputing the multiset from the stored "+
		"(outpoint-deduplicated) UTXO entries.",
		newPruningPoint, expectedCommitment, accumulatedMultiset.Hash())

	recomputedMultiset, entryCount, err := csm.recomputeImportedPruningPointMultisetFromBucket()
	if err != nil {
		return nil, err
	}
	if entryCount == 0 {
		return nil, errors.Wrapf(ruleerrors.ErrBadPruningPointUTXOSet,
			"imported pruning point %s UTXO set is empty - the transfer was truncated or the peer sent "+
				"nothing usable; another peer must supply it", newPruningPoint)
	}

	if expectedCommitment.Equal(recomputedMultiset.Hash()) {
		log.Warnf("Repaired imported pruning point %s UTXO set: a fresh multiset over the %d stored "+
			"entries matches the header commitment %s. The accumulated multiset (%s) had double-counted "+
			"one or more re-delivered chunks; using the recomputed multiset as the trust anchor.",
			newPruningPoint, entryCount, expectedCommitment, accumulatedMultiset.Hash())
		return recomputedMultiset, nil
	}

	log.Warnf("Imported pruning point %s UTXO set still does not match its header after recomputation "+
		"(header %s, fresh multiset over %d stored entries %s). The served set itself is incomplete - "+
		"the known condition where blocks disqualified upstream leave their UTXO diffs out of the "+
		"snapshotted pruning-point set. Repairing the trust anchor to the recomputed multiset so the "+
		"bucket and the per-block multiset stay consistent; blocks resolved forward from here may still "+
		"mismatch their own commitments until the upstream disqualifications are fixed.",
		newPruningPoint, expectedCommitment, entryCount, recomputedMultiset.Hash())

	return recomputedMultiset, nil
}

// recomputeImportedPruningPointMultisetFromBucket builds a fresh multiset by walking every entry
// currently stored in the imported-pruning-point-utxos bucket. Because that bucket is keyed by
// outpoint, a pair that arrived on the wire more than once is present exactly once here, so this
// result is free of the accumulation-time double-counting described in
// verifyAndRepairImportedPruningPointUTXOSet.
func (csm *consensusStateManager) recomputeImportedPruningPointMultisetFromBucket() (model.Multiset, int, error) {
	iterator, err := csm.pruningStore.ImportedPruningPointUTXOIterator(csm.databaseContext)
	if err != nil {
		return nil, 0, err
	}
	defer iterator.Close()

	recomputedMultiset := multiset.New()
	entryCount := 0
	for ok := iterator.First(); ok; ok = iterator.Next() {
		outpoint, entry, err := iterator.Get()
		if err != nil {
			return nil, 0, err
		}
		serializedUTXO, err := utxo.SerializeUTXO(entry, outpoint)
		if err != nil {
			return nil, 0, err
		}
		recomputedMultiset.Add(serializedUTXO)
		entryCount++
	}

	return recomputedMultiset, entryCount, nil
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
