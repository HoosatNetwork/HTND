package consensusstatemanager

import (
	"slices"
	"time"

	"github.com/HoosatNetwork/HTND/domain/consensus/database"
	"strings"

	"github.com/HoosatNetwork/HTND/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/utxo"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/utxosurvey"
	"github.com/HoosatNetwork/HTND/infrastructure/logger"
	"github.com/pkg/errors"

	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/ruleerrors"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/transactionhelper"
)

func (csm *consensusStateManager) CalculatePastUTXOAndAcceptanceData(stagingArea *model.StagingArea,
	blockHash *externalapi.DomainHash,
) (externalapi.UTXODiff, externalapi.AcceptanceData, model.Multiset, error) {
	onEnd := logger.LogAndMeasureExecutionTime(log, "CalculatePastUTXOAndAcceptanceData")
	defer onEnd()

	log.Debugf("CalculatePastUTXOAndAcceptanceData start for block %s", blockHash)

	if blockHash.Equal(csm.genesisHash) || blockHash.Equal(model.VirtualGenesisBlockHash) {
		blockHash = csm.genesisHash // keep lookups + logs consistent
		log.Debugf("Block %s is the genesis. By definition, "+
			"it has a predefined UTXO diff, empty acceptance data, and a predefined multiset", blockHash)

		multiset, err := csm.multisetStore.Get(csm.databaseContext, stagingArea, blockHash)
		if database.IsNotFoundError(err) {
			log.Infof("CalculatePastUTXOAndAcceptanceData failed to retrieve with %s\n", blockHash)
			return nil, nil, nil, err
		}
		if err != nil {
			return nil, nil, nil, err
		}
		utxoDiff, err := csm.utxoDiffStore.UTXODiff(csm.databaseContext, stagingArea, blockHash)
		if err != nil {
			if database.IsNotFoundError(err) {
				utxoDiff = utxo.NewUTXODiff()
			} else {
				return nil, nil, nil, err
			}
		}
		return utxoDiff, externalapi.AcceptanceData{}, multiset, nil
	}

	blockGHOSTDAGData, err := csm.ghostdagDataStore.Get(csm.databaseContext, stagingArea, blockHash, false)
	if database.IsNotFoundError(err) {
		log.Infof("CalculatePastUTXOAndAcceptanceData failed to retrieve with %s\n", blockHash)
		return nil, nil, nil, err
	}
	if err != nil {
		return nil, nil, nil, err
	}

	blockParent := blockGHOSTDAGData.SelectedParent()

	// IMPORTANT: Do NOT special-case the parent here.
	// restorePastUTXO already returns the correct genesis UTXO when parent
	// is csm.genesisHash or model.VirtualGenesisBlockHash.
	log.Debugf("Restoring the past UTXO of block %s with selectedParent %s",
		blockHash, blockParent)
	selectedParentPastUTXO, err := csm.restorePastUTXO(stagingArea, blockParent)
	if err != nil {
		return nil, nil, nil, err
	}

	log.Debugf("Restored the past UTXO of block %s with selectedParent %s. "+
		"Diff toAdd length: %d, toRemove length: %d", blockHash, blockParent,
		selectedParentPastUTXO.ToAdd().Len(), selectedParentPastUTXO.ToRemove().Len())

	return csm.calculatePastUTXOAndAcceptanceDataWithSelectedParentUTXO(
		stagingArea, blockHash, selectedParentPastUTXO, blockGHOSTDAGData)
}

func (csm *consensusStateManager) calculatePastUTXOAndAcceptanceDataWithSelectedParentUTXO(stagingArea *model.StagingArea,
	blockHash *externalapi.DomainHash, selectedParentPastUTXO externalapi.UTXODiff, blockGHOSTDAGData *externalapi.BlockGHOSTDAGData) (
	externalapi.UTXODiff, externalapi.AcceptanceData, model.Multiset, error,
) {
	if blockGHOSTDAGData == nil {
		return nil, nil, nil, errors.Errorf("blockGHOSTDAGData is nil for block %s", blockHash)
	}
	if selectedParentPastUTXO == nil {
		return nil, nil, nil, errors.Errorf("selected parent past UTXO is nil for block %s", blockHash)
	}

	// IMPORTANT: For correct UTXO commitment, the DAA score used to build UTXOEntries must match
	// the score implied by the block header. During pruning-point import/trusted flows, the DAA store
	// might not yet be fully staged for all blocks, so prefer the header's DAAScore when available.
	var daaScore uint64
	header, err := csm.blockHeaderStore.BlockHeader(csm.databaseContext, stagingArea, blockHash)
	if err != nil {
		daaScore, err = csm.daaBlocksStore.DAAScore(csm.databaseContext, stagingArea, blockHash)
		if err != nil {
			return nil, nil, nil, err
		}
	} else {
		daaScore = header.DAAScore()
	}
	log.Debugf("Calculating PastUTXO and acceptance data with DAAScore %d", daaScore)

	log.Debugf("Applying blue blocks to the selected parent past UTXO of block %s", blockHash)
	acceptanceData, utxoDiff, rejectionReasons, err := csm.applyMergeSetBlocks(stagingArea, blockHash, selectedParentPastUTXO, daaScore)
	if err != nil {
		return nil, nil, nil, err
	}
	// Stashed rather than returned: the public CalculatePastUTXOAndAcceptanceData signature is part of
	// the model interface, and this is diagnostic data that only the survey consumes. Keyed by block
	// so a concurrent resolution of another block cannot pick up these reasons.
	csm.stashRejectionReasons(blockHash, rejectionReasons)

	log.Debugf("Calculating the multiset of %s", blockHash)
	multiset, err := csm.calculateMultiset(stagingArea, blockHash, acceptanceData, blockGHOSTDAGData,
		daaScore, selectedParentPastUTXO)
	if err != nil {
		return nil, nil, nil, err
	}
	log.Debugf("The multiset of block %s resolved to: %s", blockHash, multiset.Hash())

	return utxoDiff.ToImmutable(), acceptanceData, multiset, nil
}

func (csm *consensusStateManager) restorePastUTXO(
	stagingArea *model.StagingArea, blockHash *externalapi.DomainHash,
) (externalapi.UTXODiff, error) {
	onEnd := logger.LogAndMeasureExecutionTime(log, "restorePastUTXO")
	defer onEnd()

	if blockHash.Equal(model.VirtualBlockHash) {
		return utxo.NewUTXODiff(), nil
	}

	if blockHash.Equal(csm.genesisHash) || blockHash.Equal(model.VirtualGenesisBlockHash) {
		utxoDiff, err := csm.utxoDiffStore.UTXODiff(csm.databaseContext, stagingArea, csm.genesisHash)
		if err != nil {
			if database.IsNotFoundError(err) {
				return utxo.NewUTXODiff(), nil
			}
			return nil, err
		}
		return utxoDiff, nil
	}

	log.Debugf("restorePastUTXO start for block %s", blockHash)

	// [UTXO-DEBUG] Cheap (no extra work beyond what the loop already does) walk-length/timing
	// instrumentation, surfaced only when the walk is unusually long, to find out whether
	// restorePastUTXO's walk-and-accumulate is the source of slow IBD processing/disqualification
	// handling, rather than continuing to guess. No rate limit needed - this is O(1) overhead per
	// call, not a full UTXO-set scan.
	walkStart := time.Now()

	var utxoDiffs []externalapi.UTXODiff
	var utxoDiffHashes []*externalapi.DomainHash
	nextBlockHash := blockHash
	for {
		if nextBlockHash.Equal(model.VirtualGenesisBlockHash) || nextBlockHash.Equal(csm.genesisHash) {
			log.Debugf("Block is genesis, treating as end of UTXO-diff chain for block %s", blockHash)
			break
		}

		utxoDiff, err := csm.utxoDiffStore.UTXODiff(csm.databaseContext, stagingArea, nextBlockHash)
		if err != nil {
			if database.IsNotFoundError(err) {
				log.Debugf("Block %s has no UTXO diff (not found), treating as end of UTXO-diff chain for block %s", nextBlockHash, blockHash)
				break
			}
			return nil, err
		}

		utxoDiffs = append(utxoDiffs, utxoDiff)
		utxoDiffHashes = append(utxoDiffHashes, nextBlockHash)
		log.Debugf("Collected UTXO diff for block %s: toAdd: %d, toRemove: %d",
			nextBlockHash, utxoDiff.ToAdd().Len(), utxoDiff.ToRemove().Len())

		nextBlockHash, err = csm.utxoDiffStore.UTXODiffChild(csm.databaseContext, stagingArea, nextBlockHash)
		if err != nil {
			return nil, err
		}
		if nextBlockHash == nil {
			log.Debugf("Block %s does not have a UTXO diff child, meaning we reached the virtual", nextBlockHash)
			break
		}
	}

	walkElapsed := time.Since(walkStart)
	if len(utxoDiffs) > 20 || walkElapsed > 500*time.Millisecond {
		log.Debugf("[UTXO-DEBUG] restorePastUTXO for block %s: walked %d hops from block to virtual "+
			"collecting diffs in %s - this cost is paid on every call for this block, and every "+
			"disqualified block during a cascade pays it fresh via its own restorePastUTXO call",
			blockHash, len(utxoDiffs), walkElapsed)
	}

	// apply the diffs in reverse order
	log.Debugf("Applying the collected UTXO diffs for block %s in reverse order", blockHash)
	applyStart := time.Now()
	accumulatedDiff := utxo.NewMutableUTXODiff()
	for idx, utxoDiff := range slices.Backward(utxoDiffs) {
		err := accumulatedDiff.WithDiffInPlace(utxoDiff)
		if err != nil {
			return nil, errors.Wrapf(err, "restorePastUTXO: failed to apply the UTXO diff of block %s while "+
				"walking the selected parent chain for %s (chain order, %s to virtual: %v)",
				utxoDiffHashes[idx], blockHash, blockHash, utxoDiffHashes)
		}
	}
	if applyElapsed := time.Since(applyStart); len(utxoDiffs) > 20 || applyElapsed > 500*time.Millisecond {
		log.Debugf("[UTXO-DEBUG] restorePastUTXO for block %s: merging %d collected diffs via "+
			"WithDiffInPlace took %s", blockHash, len(utxoDiffs), applyElapsed)
	}
	log.Tracef("The accumulated diff for block %s is: %s", blockHash, accumulatedDiff)
	return accumulatedDiff.ToImmutable(), nil
}

// applyMergeSetBlocks applies the block's merge set to the selected parent's past UTXO. It also
// returns, when the survey is on, why each unaccepted transaction was not accepted - see
// maybeAcceptTransaction for why that distinction is the one worth recording.
func (csm *consensusStateManager) applyMergeSetBlocks(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash,
	selectedParentPastUTXODiff externalapi.UTXODiff, daaScore uint64) (
	externalapi.AcceptanceData, externalapi.MutableUTXODiff, map[externalapi.DomainTransactionID]*transactionRejection, error,
) {
	log.Tracef("applyMergeSetBlocks start for block %s", blockHash)
	defer log.Tracef("applyMergeSetBlocks end for block %s", blockHash)

	if selectedParentPastUTXODiff == nil {
		return nil, nil, nil, errors.Errorf("selected parent past UTXO diff is nil for block %s", blockHash)
	}

	var rejectionReasons map[externalapi.DomainTransactionID]*transactionRejection
	if utxosurvey.Enabled() {
		rejectionReasons = make(map[externalapi.DomainTransactionID]*transactionRejection)
	}

	mergeSetHashes, err := csm.ghostdagManager.GetSortedMergeSet(stagingArea, blockHash)
	if err != nil {
		return nil, nil, nil, err
	}
	// VirtualGenesisBlockHash is only a marker – it has no block body.
	// It appears as SelectedParent of the pruning-point / first known blocks.
	filtered := make([]*externalapi.DomainHash, 0, len(mergeSetHashes))
	for _, h := range mergeSetHashes {
		if !h.Equal(model.VirtualGenesisBlockHash) && !h.Equal(model.VirtualBlockHash) {
			filtered = append(filtered, h)
		}
	}
	mergeSetHashes = filtered

	seenMergeSetHashes := make(map[externalapi.DomainHash]int, len(mergeSetHashes))
	for _, h := range mergeSetHashes {
		seenMergeSetHashes[*h]++
	}
	for h, count := range seenMergeSetHashes {
		if count > 1 {
			hCopy := h
			log.Debugf("[UTXO-DEBUG] applyMergeSetBlocks: block %s's own merge set contains %s %d times - "+
				"its coinbase (or any of its transactions) would be processed multiple times in this call",
				blockHash, &hCopy, count)
		}
	}

	log.Debugf("Merge set for block %s is %v", blockHash, mergeSetHashes)
	mergeSetBlocks, err := csm.blockStore.Blocks(csm.databaseContext, stagingArea, mergeSetHashes)
	if err != nil {
		return nil, nil, nil, err
	}

	selectedParentMedianTime, err := csm.pastMedianTimeManager.PastMedianTime(stagingArea, blockHash)
	if err != nil {
		return nil, nil, nil, err
	}
	log.Tracef("The past median time for block %s is: %d", blockHash, selectedParentMedianTime)

	multiblockAcceptanceData := make(externalapi.AcceptanceData, len(mergeSetBlocks))
	accumulatedUTXODiff := selectedParentPastUTXODiff.CloneMutable()
	accumulatedMass := uint64(0)

	for i, mergeSetBlock := range mergeSetBlocks {
		mergeSetBlockHash := consensushashing.BlockHash(mergeSetBlock)
		blockAcceptanceData := &externalapi.BlockAcceptanceData{
			BlockHash:                 mergeSetBlockHash,
			TransactionAcceptanceData: make([]*externalapi.TransactionAcceptanceData, len(mergeSetBlock.Transactions)),
		}
		isSelectedParent := i == 0
		log.Tracef("Is merge set block %s the selected parent: %t", mergeSetBlockHash, isSelectedParent)

		// Every UTXO created by any member of this merge set is stamped with daaScore - blockHash's
		// own DAA score - because blockHash is the block that merges them, i.e. the block that adds
		// them to the UTXO set. See utxo.AcceptedUTXOBlockDAAScore for the rule; it is a consensus
		// rule (BlockDAAScore is part of the UTXO commitment preimage), not a local choice, and
		// calculateMultiset stamps the same acceptance data with the same daaScore.
		for j, transaction := range mergeSetBlock.Transactions {
			var isAccepted bool

			var rejection *transactionRejection
			isAccepted, accumulatedMass, rejection, err = csm.maybeAcceptTransaction(stagingArea,
				transaction, blockHash, isSelectedParent, accumulatedUTXODiff, accumulatedMass,
				selectedParentMedianTime, daaScore)
			if err != nil {
				return nil, nil, nil, err
			}
			if rejectionReasons != nil && rejection != nil {
				if transactionID := consensushashing.TransactionID(transaction); transactionID != nil {
					rejectionReasons[*transactionID] = rejection
				}
			}

			var transactionInputUTXOEntries []externalapi.UTXOEntry
			if isAccepted {
				transactionInputUTXOEntries = make([]externalapi.UTXOEntry, len(transaction.Inputs))
				for k, input := range transaction.Inputs {
					transactionInputUTXOEntries[k] = input.UTXOEntry
				}
			}
			blockAcceptanceData.TransactionAcceptanceData[j] = &externalapi.TransactionAcceptanceData{
				Transaction:                 transaction,
				Fee:                         transaction.Fee,
				IsAccepted:                  isAccepted,
				TransactionInputUTXOEntries: transactionInputUTXOEntries,
			}

		}
		multiblockAcceptanceData[i] = blockAcceptanceData
	}

	return multiblockAcceptanceData, accumulatedUTXODiff, rejectionReasons, nil
}

// maybeAcceptTransaction decides whether one merge-set transaction is accepted, and - when it is not
// - says why.
//
// The reason matters far more than it looks. A transaction rejected because an input could not be
// found is indistinguishable, in the acceptance data and in every log, from one rejected because it
// broke a rule; but the first kind is how a UTXO gap SPREADS. The missing input makes this
// transaction unaccepted, so the outputs it would have created never enter the set either, so every
// later transaction spending those outputs is rejected in turn. A survey of mainnet found 3,470 of
// 9,076 missing coins had a creating transaction this node itself had rejected, which is that
// cascade, and nothing in the node distinguished it from ordinary rejection.
//
// rejectionReason is empty when the transaction is accepted, and is only populated when the UTXO
// survey is enabled - it is diagnostic, and never affects the verdict.
func (csm *consensusStateManager) maybeAcceptTransaction(
	stagingArea *model.StagingArea,
	transaction *externalapi.DomainTransaction,
	blockHash *externalapi.DomainHash,
	isSelectedParent bool,
	accumulatedUTXODiff externalapi.MutableUTXODiff,
	accumulatedMassBefore uint64,
	_ int64,
	blockDAAScore uint64,
) (isAccepted bool, accumulatedMassAfter uint64, rejection *transactionRejection, err error) {
	if transaction == nil {
		log.Errorf("maybeAcceptTransaction called with nil transaction for block %s", blockHash)
		return false, accumulatedMassBefore, nil, errors.New("nil transaction passed to maybeAcceptTransaction")
	}
	transactionID := "<nil>"
	transactionIDPtr := consensushashing.TransactionID(transaction)
	if transactionIDPtr != nil {
		transactionID = transactionIDPtr.String()
	}
	log.Tracef("maybeAcceptTransaction start for transaction %s in block %s", transactionID, blockHash)
	defer log.Tracef("maybeAcceptTransaction end for transaction %s in block %s", transactionID, blockHash)

	log.Tracef("Populating transaction %s with UTXO entries", transactionID)
	err = csm.populateTransactionWithUTXOEntriesFromVirtualOrDiff(stagingArea, transaction, accumulatedUTXODiff.ToImmutable())
	if err != nil {
		csm.noteAcceptanceRejection(blockHash, transactionIDPtr, err)
		// The cascade path. Not an error here by design - the transaction simply cannot be accepted
		// against a set that lacks its input - but it is the one rejection reason that means this
		// node's own gap just got bigger.
		return false, accumulatedMassBefore, newTransactionRejection("missing-input", err), nil
	}

	// Coinbase transaction outputs are added to the UTXO-set only if they are in the selected parent chain.
	isCoinbase := transactionhelper.IsCoinBase(transaction)
	if isCoinbase {
		if !isSelectedParent {
			log.Tracef("Transaction %s is the coinbase of block %s "+
				"but said block is not in the selected parent chain. "+
				"As such, it is not accepted", transactionID, blockHash)
			return false, accumulatedMassBefore, newTransactionRejection("coinbase-not-on-selected-chain", nil), nil
		}
		log.Tracef("Transaction %s is the coinbase of block %s", transactionID, blockHash)
	} else {
		log.Tracef("Validating transaction %s in block %s", transactionID, blockHash)
		err = csm.transactionValidator.ValidateTransactionInContextAndPopulateFee(
			stagingArea, transaction, blockHash, blockDAAScore)
		if err != nil {
			if !errors.As(err, &(ruleerrors.RuleError{})) {
				return false, 0, nil, err
			}

			log.Tracef("Validation failed for transaction %s "+
				"in block %s: %s", transactionID, blockHash, err)
			return false, accumulatedMassBefore, newTransactionRejection("rule-error", err), nil
		}
		log.Tracef("Validation passed for transaction %s in block %s", transactionID, blockHash)
	}

	var coinbasePreExistingInToRemove []int
	if isCoinbase && transactionIDPtr != nil {
		for i := range transaction.Outputs {
			outpoint := externalapi.NewDomainOutpoint(transactionIDPtr, uint32(i))
			if accumulatedUTXODiff.ToRemove().Contains(outpoint) {
				coinbasePreExistingInToRemove = append(coinbasePreExistingInToRemove, i)
			}
		}
	}

	log.Tracef("Adding transaction %s in block %s to the accumulated diff", transactionID, blockHash)
	// blockDAAScore is the merging block's DAA score, which is what the UTXOEntries this transaction
	// creates are stamped with - see the comment at the call site in applyMergeSetBlocks and
	// utxo.AcceptedUTXOBlockDAAScore.
	err = accumulatedUTXODiff.AddTransaction(transaction, utxo.AcceptedUTXOBlockDAAScore(blockDAAScore))
	if err != nil {
		// Do NOT swallow this into isAccepted=false: transaction was already validated above (or is
		// the selected parent's coinbase), so a failure here is not a legitimate rejection - it's
		// AddTransaction/addEntry/removeEntry finding an outpoint collision that shouldn't exist for a
		// correctly-accepted transaction (e.g. the DAA-score-stamping inconsistency fixed in 96efc0d3d/
		// 19fb5b15a, or unknown corruption). Worse, AddTransaction mutates accumulatedUTXODiff
		// input-by-input and output-by-output with no rollback on a mid-way failure, so by the time it
		// returns an error accumulatedUTXODiff may already hold a partial application of this
		// transaction - continuing and merely marking it "not accepted" would silently persist that
		// partial, inconsistent diff as this block's official acceptance data. Abort instead of writing
		// corrupted acceptance data to disk.
		return false, 0, nil, errors.Wrapf(err, "failed to add transaction %s in block %s to accumulated diff",
			transactionID, blockHash)
	}

	if isCoinbase && transactionIDPtr != nil {
		for i := range transaction.Outputs {
			outpoint := externalapi.NewDomainOutpoint(transactionIDPtr, uint32(i))
			if _, ok := accumulatedUTXODiff.ToAdd().Get(outpoint); !ok {
				wasPreExisting := slices.Contains(coinbasePreExistingInToRemove, i)
				if wasPreExisting {
					// Explained: addEntry's toRemove collision path (mutable_utxo_diff.go) already
					// accounts for this - either a benign same-valued cancel (this coinbase is
					// byte-identical to one already accounted for, e.g. the same mining template
					// producing multiple valid nonces before being refreshed) or a genuine mismatch,
					// which addEntry itself already logs. Nothing new to report here.
					log.Debugf("[UTXO-DEBUG] coinbase tx %s output %d in block %s: MISSING from "+
						"accumulatedUTXODiff.ToAdd(), but was already in ToRemove before the call "+
						"(explained by addEntry's own logging), daaScore=%d",
						transactionID, i, blockHash, blockDAAScore)
				} else {
					// Not explained by the toRemove-collision mechanism at all - this output is
					// missing from ToAdd() despite AddTransaction succeeding and no pre-existing
					// toRemove entry for it. Worth investigating if this ever fires.
					log.Debugf("[UTXO-DEBUG] coinbase tx %s output %d in block %s: MISSING from "+
						"accumulatedUTXODiff.ToAdd() after AddTransaction returned no error, and it was "+
						"NOT already present in ToRemove before the call - not explained by the known "+
						"toRemove-collision mechanism, daaScore=%d",
						transactionID, i, blockHash, blockDAAScore)
				}
			}
		}
	}

	return true, accumulatedMassAfter, nil, nil
}

// transactionRejection is why one merge-set transaction was not accepted, and - for the case that
// matters - which coins it went looking for and could not find.
//
// MissingOutpoints is what makes the cascade traceable. A coin absent from this node's set stops a
// transaction being accepted, so the coins that transaction would have created are absent too, and
// the same thing happens to whatever would have spent those. Knowing only that a transaction was
// rejected gives you one link; knowing which outpoint it could not find lets the chain be walked
// back to whatever went missing first, which is the only thing on that chain worth fixing.
type transactionRejection struct {
	reason           string
	missingOutpoints []*externalapi.DomainOutpoint
}

// newTransactionRejection names why a transaction was not accepted, cheaply and only when the survey
// is on. A missing input keeps its own label rather than the rule name it arrives under, because
// that is the distinction the whole measurement exists to make.
func newTransactionRejection(kind string, err error) *transactionRejection {
	if !utxosurvey.Enabled() {
		return nil
	}
	var missingTxOut ruleerrors.ErrMissingTxOut
	if errors.As(err, &missingTxOut) {
		// A transaction spending a coin this view has already spent is rejected for being a double
		// spend, which is correct and expected on a DAG - the same transaction sits in several blocks
		// and only the first merge accepts it. Counting those as "missing-input" made ordinary
		// duplicate handling indistinguishable from this node lacking a coin, and a survey of a live
		// sync read 82% of absent coins as self-inflicted damage on the strength of that conflation.
		if missingTxOut.HasDoubleSpend() {
			return &transactionRejection{reason: "double-spend", missingOutpoints: missingTxOut.SpentOutpoints}
		}
		return &transactionRejection{reason: "missing-input", missingOutpoints: missingTxOut.MissingOutpoints}
	}
	var ruleError ruleerrors.RuleError
	if errors.As(err, &ruleError) {
		if name, _, _ := strings.Cut(ruleError.Error(), ": "); name != "" {
			return &transactionRejection{reason: name}
		}
	}
	return &transactionRejection{reason: kind}
}

// RestorePastUTXOSetIterator restores the given block's UTXOSet iterator, and returns it as a externalapi.ReadOnlyUTXOSetIterator
func (csm *consensusStateManager) RestorePastUTXOSetIterator(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash) (
	externalapi.ReadOnlyUTXOSetIterator, error,
) {
	onEnd := logger.LogAndMeasureExecutionTime(log, "RestorePastUTXOSetIterator")
	defer onEnd()

	blockStatus, _, err := csm.ResolveBlockStatus(stagingArea, blockHash, true)
	if err != nil {
		return nil, err
	}
	if blockStatus != externalapi.StatusUTXOValid {
		return nil, errors.Errorf(
			"block %s has status '%s' and therefore can't restore its UTXO set; only blocks with status '%s' can be restored",
			blockHash, blockStatus, externalapi.StatusUTXOValid)
	}

	log.Tracef("RestorePastUTXOSetIterator start for block %s", blockHash)
	defer log.Tracef("RestorePastUTXOSetIterator end for block %s", blockHash)

	log.Debugf("Calculating UTXO diff for block %s", blockHash)
	blockDiff, err := csm.restorePastUTXO(stagingArea, blockHash)
	if err != nil {
		return nil, err
	}

	virtualUTXOSetIterator, err := csm.consensusStateStore.VirtualUTXOSetIterator(csm.databaseContext, stagingArea)
	if err != nil {
		return nil, err
	}

	return utxo.IteratorWithDiff(virtualUTXOSetIterator, blockDiff)
}
