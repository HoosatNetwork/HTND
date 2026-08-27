package consensusstatemanager

import (
	"math"

	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/multiset"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/utxo"
	"github.com/pkg/errors"
)

func (csm *consensusStateManager) calculateMultiset(stagingArea *model.StagingArea,
	blockHash *externalapi.DomainHash,
	acceptanceData externalapi.AcceptanceData,
	blockGHOSTDAGData *externalapi.BlockGHOSTDAGData,
	daaScore uint64,
) (model.Multiset, error) {
	log.Tracef("calculateMultiset start for block with selected parent %s", blockGHOSTDAGData.SelectedParent())
	defer log.Tracef("calculateMultiset end for block with selected parent %s", blockGHOSTDAGData.SelectedParent())

	// Case 1: we are calculating the multiset of the genesis / virtual-genesis itself
	if blockHash.Equal(csm.genesisHash) || blockHash.Equal(model.VirtualGenesisBlockHash) {
		log.Debugf("Selected parent is nil, which could only happen for the genesis. " +
			"The genesis has a predefined multiset")
		return csm.multisetStore.Get(csm.databaseContext, stagingArea, csm.genesisHash)
	}

	// Case 2: normal path – but the selected parent may be the virtual-genesis marker
	selectedParent := blockGHOSTDAGData.SelectedParent()
	if selectedParent.Equal(model.VirtualGenesisBlockHash) || selectedParent.Equal(csm.genesisHash) {
		// VirtualGenesis is only a marker and never has its own multiset.
		// Fall back to the real genesis multiset (same idea as restorePastUTXO).
		csm.multisetStore.Stage(stagingArea, csm.genesisHash, multiset.New())
		selectedParent = csm.genesisHash
	}

	ms, err := csm.multisetStore.Get(csm.databaseContext, stagingArea, selectedParent)
	if err != nil {
		return nil, err
	}
	log.Debugf("The multiset for the selected parent %s is: %s", selectedParent, ms.Hash())

	for _, blockAcceptanceData := range acceptanceData {
		for i, transactionAcceptanceData := range blockAcceptanceData.TransactionAcceptanceData {
			transaction := transactionAcceptanceData.Transaction
			transactionID := consensushashing.TransactionID(transaction)
			if !transactionAcceptanceData.IsAccepted {
				log.Tracef("Skipping transaction %s because it was not accepted", transactionID)
				continue
			}

			isCoinbase := i == 0
			log.Tracef("Is transaction %s a coinbase transaction: %t", transactionID, isCoinbase)

			err := addTransactionToMultiset(ms, transaction, daaScore, isCoinbase)
			if err != nil {
				return nil, err
			}
			log.Tracef("Added transaction %s to the multiset", transactionID)
		}
	}

	return ms, nil
}

// reverseMultiset applies the reverse of the given acceptance data to a multiset.
// This undoes the effect of calculateMultiset / addTransactionToMultiset for the
// provided acceptance data (remove outputs, re-add inputs).
func (csm *consensusStateManager) reverseMultiset(ms model.Multiset,
	acceptanceData externalapi.AcceptanceData,
	daaScore uint64,
) error {
	log.Tracef("reverseMultiset start")
	defer log.Tracef("reverseMultiset end")

	// Process in reverse order of acceptance data so that the net effect is an exact inverse.
	for i := len(acceptanceData) - 1; i >= 0; i-- {
		blockAcceptanceData := acceptanceData[i]
		for j := len(blockAcceptanceData.TransactionAcceptanceData) - 1; j >= 0; j-- {
			transactionAcceptanceData := blockAcceptanceData.TransactionAcceptanceData[j]
			transaction := transactionAcceptanceData.Transaction
			transactionID := consensushashing.TransactionID(transaction)
			if !transactionAcceptanceData.IsAccepted {
				log.Tracef("Skipping transaction %s because it was not accepted", transactionID)
				continue
			}

			isCoinbase := j == 0
			log.Tracef("Is transaction %s a coinbase transaction: %t", transactionID, isCoinbase)

			err := removeTransactionFromMultiset(ms, transaction, daaScore, isCoinbase)
			if err != nil {
				return err
			}
			log.Tracef("Removed transaction %s from the multiset", transactionID)
		}
	}

	return nil
}

func addTransactionToMultiset(multiset model.Multiset, transaction *externalapi.DomainTransaction,
	blockDAAScore uint64, isCoinbase bool,
) error {
	transactionID := consensushashing.TransactionID(transaction)
	log.Tracef("addTransactionToMultiset start for transaction %s", transactionID)
	defer log.Tracef("addTransactionToMultiset end for transaction %s", transactionID)

	for _, input := range transaction.Inputs {
		log.Tracef("Removing input %s at index %d from the multiset",
			input.PreviousOutpoint.TransactionID, input.PreviousOutpoint.Index)
		err := removeUTXOFromMultiset(multiset, input.UTXOEntry, &input.PreviousOutpoint)
		if err != nil {
			return err
		}
	}

	for i, output := range transaction.Outputs {
		if i < 0 || i > math.MaxUint32 {
			return errors.Errorf("output index %d cannot be represented as uint32", i)
		}
		outpoint := &externalapi.DomainOutpoint{
			TransactionID: *transactionID,
			Index:         uint32(i),
		}
		utxoEntry := utxo.NewUTXOEntry(output.Value, output.ScriptPublicKey, isCoinbase, blockDAAScore)

		log.Tracef("Adding output %s at index %d to the multiset", transactionID, i)
		err := addUTXOToMultiset(multiset, utxoEntry, outpoint)
		if err != nil {
			return err
		}
	}

	return nil
}

// removeTransactionFromMultiset is the exact inverse of addTransactionToMultiset.
// It removes the transaction's outputs from the multiset and re-adds its inputs.
func removeTransactionFromMultiset(multiset model.Multiset, transaction *externalapi.DomainTransaction,
	blockDAAScore uint64, isCoinbase bool,
) error {
	transactionID := consensushashing.TransactionID(transaction)
	log.Tracef("removeTransactionFromMultiset start for transaction %s", transactionID)
	defer log.Tracef("removeTransactionFromMultiset end for transaction %s", transactionID)

	// Reverse of add: first remove the outputs that were added.
	for i, output := range transaction.Outputs {
		if i < 0 || i > math.MaxUint32 {
			return errors.Errorf("output index %d cannot be represented as uint32", i)
		}
		outpoint := &externalapi.DomainOutpoint{
			TransactionID: *transactionID,
			Index:         uint32(i),
		}
		utxoEntry := utxo.NewUTXOEntry(output.Value, output.ScriptPublicKey, isCoinbase, blockDAAScore)

		log.Tracef("Removing output %s at index %d from the multiset", transactionID, i)
		err := removeUTXOFromMultiset(multiset, utxoEntry, outpoint)
		if err != nil {
			return err
		}
	}

	// Then re-add the inputs that were previously removed.
	for _, input := range transaction.Inputs {
		log.Tracef("Adding input %s at index %d back to the multiset",
			input.PreviousOutpoint.TransactionID, input.PreviousOutpoint.Index)
		err := addUTXOToMultiset(multiset, input.UTXOEntry, &input.PreviousOutpoint)
		if err != nil {
			return err
		}
	}

	return nil
}

func addUTXOToMultiset(multiset model.Multiset, entry externalapi.UTXOEntry,
	outpoint *externalapi.DomainOutpoint,
) error {
	serializedUTXO, err := utxo.SerializeUTXO(entry, outpoint)
	if err != nil {
		return err
	}
	multiset.Add(serializedUTXO)

	return nil
}

func removeUTXOFromMultiset(multiset model.Multiset, entry externalapi.UTXOEntry,
	outpoint *externalapi.DomainOutpoint,
) error {
	serializedUTXO, err := utxo.SerializeUTXO(entry, outpoint)
	if err != nil {
		return err
	}
	multiset.Remove(serializedUTXO)

	return nil
}

// verifyMultisetSelfConsistency independently rebuilds a fresh multiset by iterating the actual,
// absolute UTXO set implied by diff - combining virtual's own ground-truth UTXO table
// (consensusStateStore, maintained as a real table via updateVirtualWithParents, entirely separate
// from the incremental diff-chain bookkeeping calculateMultiset uses) with diff itself - and
// compares its hash against incrementalMultiset's, AND against blockHash's own header.UTXOCommitment()
// - the value that actually decides pass/fail in validateUTXOCommitment. Comparing incremental vs
// fresh alone only proves internal self-consistency; it says nothing about which one (if either) is
// what the network actually agreed on for this block, so both get checked against the header
// directly here. This is O(virtual UTXO set size), so only call it on an actual verification
// failure, never in the hot path.
func (csm *consensusStateManager) verifyMultisetSelfConsistency(stagingArea *model.StagingArea,
	label string, blockHash *externalapi.DomainHash, diff externalapi.UTXODiff, incrementalMultiset model.Multiset,
) {
	var expectedCommitment *externalapi.DomainHash
	if header, err := csm.blockHeaderStore.BlockHeader(csm.databaseContext, stagingArea, blockHash); err != nil {
		log.Warnf("[UTXO-DEBUG] %s (%s): could not fetch header to get expected UTXOCommitment: %s", label, blockHash, err)
	} else {
		expectedCommitment = header.UTXOCommitment()
	}
	virtualIterator, err := csm.consensusStateStore.VirtualUTXOSetIterator(csm.databaseContext, stagingArea)
	if err != nil {
		log.Warnf("[UTXO-DEBUG] %s (%s): could not get virtual UTXO set iterator for self-consistency check: %s",
			label, blockHash, err)
		return
	}
	defer virtualIterator.Close()

	iterator, err := utxo.IteratorWithDiff(virtualIterator, diff)
	if err != nil {
		log.Warnf("[UTXO-DEBUG] %s (%s): could not build diff iterator for self-consistency check: %s",
			label, blockHash, err)
		return
	}
	defer iterator.Close()

	fresh := multiset.New()
	entryCount := 0
	for ok := iterator.First(); ok; ok = iterator.Next() {
		outpoint, entry, err := iterator.Get()
		if err != nil {
			log.Warnf("[UTXO-DEBUG] %s (%s): iterator.Get failed during self-consistency check: %s",
				label, blockHash, err)
			return
		}
		serialized, err := utxo.SerializeUTXO(entry, outpoint)
		if err != nil {
			log.Warnf("[UTXO-DEBUG] %s (%s): SerializeUTXO failed during self-consistency check: %s",
				label, blockHash, err)
			return
		}
		fresh.Add(serialized)
		entryCount++
	}

	freshHash := fresh.Hash()
	incrementalHash := incrementalMultiset.Hash()
	agree := freshHash.Equal(incrementalHash)
	incrementalMatchesHeader := expectedCommitment != nil && incrementalHash.Equal(expectedCommitment)
	freshMatchesHeader := expectedCommitment != nil && freshHash.Equal(expectedCommitment)

	log.Warnf("[UTXO-DEBUG] %s (%s): entries=%d | header expects=%s | incremental=%s (matchesHeader=%t) | "+
		"freshFromActualSet=%s (matchesHeader=%t) | incrementalAgreesWithFresh=%t",
		label, blockHash, entryCount, expectedCommitment, incrementalHash, incrementalMatchesHeader,
		freshHash, freshMatchesHeader, agree)

	switch {
	case agree && incrementalMatchesHeader:
		log.Warnf("[UTXO-DEBUG] %s (%s): incremental and fresh recomputation agree with each other AND "+
			"with the header - this block's own multiset is fully correct.", label, blockHash)
	case agree && !incrementalMatchesHeader:
		log.Warnf("[UTXO-DEBUG] %s (%s): incremental and fresh recomputation agree with EACH OTHER but "+
			"NEITHER matches the header - not an Add/Remove accounting bug; the accepted UTXO set itself "+
			"(which transactions got accepted/rejected, or an ancestor's already-wrong stored multiset "+
			"that both computations build on top of) differs from what the network agreed on.",
			label, blockHash)
	case !agree && freshMatchesHeader:
		log.Warnf("[UTXO-DEBUG] %s (%s): fresh recomputation from the actual UTXO set MATCHES the header "+
			"but the incrementally-maintained multiset does NOT - proves the incremental Add/Remove "+
			"bookkeeping (calculateMultiset/addTransactionToMultiset) has drifted away from the actual, "+
			"correct UTXO set. This is the Add/Remove accounting bug.", label, blockHash)
	case !agree && incrementalMatchesHeader:
		log.Warnf("[UTXO-DEBUG] %s (%s): incremental multiset MATCHES the header but the fresh "+
			"recomputation from the actual UTXO set does NOT - the virtual UTXO set/diff itself disagrees "+
			"with the (correct) incremental multiset history; look at what diff/virtual actually contains "+
			"for this block, not the multiset code.", label, blockHash)
	default:
		log.Warnf("[UTXO-DEBUG] %s (%s): incremental and fresh recomputation DISAGREE with each other, "+
			"and NEITHER matches the header - drift in the bookkeeping AND the underlying set is wrong "+
			"relative to the network.", label, blockHash)
	}
}

// verifyAcceptanceDataAgainstDiff cross-checks, entry by entry, that acceptanceData and diff agree
// on every single UTXO produced by this block's resolution. They're built by two structurally
// separate implementations walking the exact same acceptanceData - applyMergeSetBlocks builds diff
// via MutableUTXODiff.AddTransaction, calculateMultiset builds the multiset via its own
// addTransactionToMultiset - so this is the direct test of whether those two implementations still
// agree, checking the actual amount/script/isCoinbase/daaScore values, not just aggregate hashes.
// Prints every specific outpoint where they don't, which a hash comparison alone can't do.
func (csm *consensusStateManager) verifyAcceptanceDataAgainstDiff(label string, blockHash *externalapi.DomainHash,
	acceptanceData externalapi.AcceptanceData, diff externalapi.UTXODiff, daaScore uint64,
) {
	mismatches := 0
	for _, blockAcceptanceData := range acceptanceData {
		for i, txAcceptance := range blockAcceptanceData.TransactionAcceptanceData {
			if !txAcceptance.IsAccepted {
				continue
			}
			transaction := txAcceptance.Transaction
			isCoinbase := i == 0
			transactionID := consensushashing.TransactionID(transaction)

			for outIdx, output := range transaction.Outputs {
				outpoint := &externalapi.DomainOutpoint{TransactionID: *transactionID, Index: uint32(outIdx)}
				entry, ok := diff.ToAdd().Get(outpoint)
				if !ok {
					mismatches++
					log.Warnf("[UTXO-DEBUG] %s (%s): accepted tx %s output %d is MISSING from diff.ToAdd() - "+
						"the multiset would add it (amount=%d script=%x isCoinbase=%t daaScore=%d) but diff doesn't have it",
						label, blockHash, transactionID, outIdx, output.Value, output.ScriptPublicKey.Script, isCoinbase, daaScore)
					continue
				}
				if entry.Amount() != output.Value || !entry.ScriptPublicKey().Equal(output.ScriptPublicKey) ||
					entry.IsCoinbase() != isCoinbase || entry.BlockDAAScore() != daaScore {
					mismatches++
					log.Warnf("[UTXO-DEBUG] %s (%s): accepted tx %s output %d MISMATCH - diff.ToAdd() has "+
						"amount=%d script=%x isCoinbase=%t daaScore=%d, but the multiset would add "+
						"amount=%d script=%x isCoinbase=%t daaScore=%d",
						label, blockHash, transactionID, outIdx,
						entry.Amount(), entry.ScriptPublicKey().Script, entry.IsCoinbase(), entry.BlockDAAScore(),
						output.Value, output.ScriptPublicKey.Script, isCoinbase, daaScore)
				}
			}

			for _, input := range transaction.Inputs {
				if !diff.ToRemove().Contains(&input.PreviousOutpoint) {
					if diff.ToAdd().Contains(&input.PreviousOutpoint) {
						mismatches++
						log.Warnf("[UTXO-DEBUG] %s (%s): accepted tx %s input %s:%d is MISSING from "+
							"diff.ToRemove() AND still present in diff.ToAdd() - the multiset would remove it "+
							"but diff never actually removed it", label, blockHash, transactionID,
							input.PreviousOutpoint.TransactionID, input.PreviousOutpoint.Index)
						continue
					}
					// Absent from BOTH toAdd and toRemove is the expected signature of a legitimate
					// net-zero cancellation: this outpoint was created and spent within the same
					// accumulated diff (populateTransactionWithUTXOEntriesFromVirtualOrDiff populates
					// an input's value directly from diff.ToAdd() when present there, so the value
					// removeEntry compares against is guaranteed identical to what's already in
					// toAdd - see addEntry/removeEntry's own toAdd/toRemove-collision handling in
					// mutable_utxo_diff.go). Not a bug - don't flag it.
				}
			}
		}
	}

	if mismatches == 0 {
		log.Warnf("[UTXO-DEBUG] %s (%s): acceptanceData vs diff entry-level check PASSED - every accepted "+
			"transaction's effect matches exactly between diff.ToAdd()/ToRemove() and what the multiset "+
			"would independently compute from the same acceptanceData.", label, blockHash)
	} else {
		log.Warnf("[UTXO-DEBUG] %s (%s): acceptanceData vs diff entry-level check found %d mismatch(es) - "+
			"see the specific outpoints logged above.", label, blockHash, mismatches)
	}
}
