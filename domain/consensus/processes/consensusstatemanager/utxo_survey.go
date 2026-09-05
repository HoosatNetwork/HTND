package consensusstatemanager

import (
	"encoding/hex"
	"fmt"
	"strings"
	"sync"

	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/ruleerrors"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/multiset"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/utxo"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/utxosurvey"
	"github.com/pkg/errors"
)

// blockSurvey accumulates every UTXO-verification failure of a single block while verifyUTXO runs,
// so that one record covering all of them can be written afterwards. Without it the node keeps only
// the first failure: verifyUTXO returns on the first failing step, logToleratedIssue warns once per
// step label for the whole process, and validateBlockTransactionsAgainstPastUTXO abandons the
// remaining transactions as soon as one of them is missing an input.
//
// A nil *blockSurvey means the survey is off; every method is nil-safe so call sites need no
// guards.
type blockSurvey struct {
	mu    sync.Mutex
	stage string

	// steps holds one entry per failed check, in the order the checks ran.
	steps []surveyStepFailure

	// missing is keyed by outpoint so that the same outpoint spent by several transactions in the
	// block appears once; missingOrder keeps the output deterministic.
	missing      map[externalapi.DomainOutpoint]*surveyMissingOutpoint
	missingOrder []externalapi.DomainOutpoint
}

type surveyStepFailure struct {
	step string
	err  error
}

type surveyMissingOutpoint struct {
	outpoint externalapi.DomainOutpoint
	spentBy  string
}

// newBlockSurvey returns a survey for one block, or nil when HTND_UTXO_SURVEY is unset or the
// record cap has been reached - in which case every method below is a no-op and no work is done.
func newBlockSurvey(stage string) *blockSurvey {
	if !utxosurvey.Enabled() {
		return nil
	}
	return &blockSurvey{
		stage:   stage,
		missing: make(map[externalapi.DomainOutpoint]*surveyMissingOutpoint),
	}
}

// active reports whether the survey is on. Call sites use it to decide whether to do survey-only
// extra work, such as continuing past a failure that validation itself would have stopped at.
func (bs *blockSurvey) active() bool {
	return bs != nil
}

// noteFailure records that one named check failed, whether or not the failure was tolerated.
func (bs *blockSurvey) noteFailure(step string, err error) {
	if bs == nil || err == nil {
		return
	}
	bs.mu.Lock()
	defer bs.mu.Unlock()
	bs.steps = append(bs.steps, surveyStepFailure{step: step, err: err})
}

// noteMissingOutpointsFromError records every outpoint named by an ErrMissingTxOut, attributed to
// the transaction that tried to spend them. Errors that are not ErrMissingTxOut are ignored here -
// noteFailure has already recorded them.
func (bs *blockSurvey) noteMissingOutpointsFromError(spendingTransactionID string, err error) {
	if bs == nil || err == nil {
		return
	}
	var missingTxOut ruleerrors.ErrMissingTxOut
	if !errors.As(err, &missingTxOut) {
		return
	}

	bs.mu.Lock()
	defer bs.mu.Unlock()
	for _, outpoint := range missingTxOut.MissingOutpoints {
		if outpoint == nil {
			continue
		}
		if _, alreadySeen := bs.missing[*outpoint]; alreadySeen {
			continue
		}
		bs.missing[*outpoint] = &surveyMissingOutpoint{outpoint: *outpoint, spentBy: spendingTransactionID}
		bs.missingOrder = append(bs.missingOrder, *outpoint)
	}
}

// failed reports whether anything at all was recorded for this block.
func (bs *blockSurvey) failed() bool {
	if bs == nil {
		return false
	}
	bs.mu.Lock()
	defer bs.mu.Unlock()
	return len(bs.steps) > 0 || len(bs.missing) > 0
}

// errorLabel joins every failed check's identifying name, e.g. "ErrBadUTXOCommitment+missing-input".
func (bs *blockSurvey) errorLabel() string {
	seen := make(map[string]struct{}, len(bs.steps))
	labels := make([]string, 0, len(bs.steps))
	for _, failure := range bs.steps {
		label := surveyErrorLabel(failure.step, failure.err)
		if _, duplicate := seen[label]; duplicate {
			continue
		}
		seen[label] = struct{}{}
		labels = append(labels, label)
	}
	if len(labels) == 0 && len(bs.missing) > 0 {
		labels = append(labels, "missing-input")
	}
	return strings.Join(labels, "+")
}

// surveyErrorLabel names the consensus rule that fired, falling back to the step label.
//
// It reads the name out of RuleError.Error() rather than comparing against the ruleerrors
// sentinels, because errors.Is is not safe on these: RuleError is only *statically* comparable -
// its inner field is an interface, and one carrying an ErrMissingTxOut (whose MissingOutpoints is a
// slice) panics the moment errors.Is evaluates err == target.
func surveyErrorLabel(step string, err error) string {
	var missingTxOut ruleerrors.ErrMissingTxOut
	if errors.As(err, &missingTxOut) {
		return "missing-input"
	}
	var ruleError ruleerrors.RuleError
	if errors.As(err, &ruleError) {
		// RuleError.Error() is "<name>" when it has no inner error and "<name>: <inner>" when it does.
		if name, _, _ := strings.Cut(ruleError.Error(), ": "); name != "" {
			return name
		}
	}
	return step
}

// recordBlockSurvey turns everything gathered about one failing block into a single JSONL record,
// including the store lookups that decide whether each missing outpoint is an original coin, a coin
// this block should have created, or a coin that exists under different identity bytes.
//
// selectedParentPastUTXO may be nil (the caller does not always have it); the classification then
// falls back to the virtual UTXO set alone and says so in the notes.
func (csm *consensusStateManager) recordBlockSurvey(stagingArea *model.StagingArea, survey *blockSurvey,
	block *externalapi.DomainBlock, blockHash, selectedParentHash *externalapi.DomainHash,
	selectedParentPastUTXO, pastUTXODiff externalapi.UTXODiff, acceptanceData externalapi.AcceptanceData,
	blockMultiset model.Multiset,
) {
	if !survey.failed() {
		return
	}
	survey.mu.Lock()
	defer survey.mu.Unlock()

	record := &utxosurvey.Record{
		BlockHash:      blockHash.String(),
		IBDStage:       survey.stage,
		Error:          survey.errorLabel(),
		Classification: utxosurvey.ClassificationUnknown,
	}
	var notes []string

	if block != nil && block.Header != nil {
		record.DAAScore = block.Header.DAAScore()
		record.HeaderUTXOCommitment = block.Header.UTXOCommitment().String()
	}
	if blockMultiset != nil {
		record.CalculatedUTXOCommitment = blockMultiset.Hash().String()
	}
	if block != nil && len(block.Transactions) > 0 {
		record.CoinbaseTxID = consensushashing.TransactionID(block.Transactions[0]).String()
	}

	if ghostdagData, err := csm.ghostdagDataStore.Get(csm.databaseContext, stagingArea, blockHash, false); err == nil {
		record.BlueScore = ghostdagData.BlueScore()
		if selectedParentHash == nil {
			selectedParentHash = ghostdagData.SelectedParent()
		}
	}
	if selectedParentHash != nil {
		record.SelectedParent = selectedParentHash.String()
		if parentMultiset, err := csm.multisetStore.Get(csm.databaseContext, stagingArea, selectedParentHash); err == nil {
			record.ParentStoredMultiset = parentMultiset.Hash().String()
		}
		if parentHeader, err := csm.blockHeaderStore.BlockHeader(csm.databaseContext, stagingArea, selectedParentHash); err == nil {
			record.ParentHeaderUTXOCommitment = parentHeader.UTXOCommitment().String()
		}
	}

	// The headers selected chain is the node's own view of which blocks are chain blocks; a block
	// absent from it was resolved off the selected chain.
	if _, err := csm.headersSelectedChainStore.GetIndexByHash(csm.databaseContext, stagingArea, blockHash); err == nil {
		record.IsChainBlock = true
	}

	// Recomputing the parent's multiset from the actual UTXO set is an O(UTXO-set) scan - minutes on
	// a mature chain - so it is rationed by HTND_UTXO_SURVEY_DEEP rather than run per failing block.
	// It is the check that separates "the parent's stored multiset drifted from its own UTXO set"
	// from "the parent's set is right and this block's arithmetic is wrong".
	if selectedParentPastUTXO != nil && utxosurvey.TakeDeepBudget() {
		if recomputed, err := csm.recomputeMultisetFromActualSet(stagingArea, selectedParentPastUTXO); err == nil {
			record.ParentRecomputedMultiset = recomputed.String()
		} else {
			notes = append(notes, fmt.Sprintf("parent multiset recomputation failed: %s", err))
		}
	}

	createdByAcceptance := summarizeAcceptance(record, acceptanceData)

	record.MissingOutpoints = csm.surveyMissingOutpoints(stagingArea, survey, record.DAAScore,
		selectedParentPastUTXO, pastUTXODiff, createdByAcceptance)

	if selectedParentPastUTXO != nil {
		adds, removes, err := surveyBlockDelta(selectedParentPastUTXO, pastUTXODiff, acceptanceData, record.DAAScore)
		if err != nil {
			notes = append(notes, fmt.Sprintf("block delta comparison failed: %s", err))
		} else {
			record.ExtraAddsNotInHeaderView = adds
			record.ExtraRemovesNotInHeaderView = removes
		}
	} else {
		notes = append(notes, "selected parent past UTXO unavailable: foundInParentSet falls back to the "+
			"virtual UTXO set, and the block's own add/remove delta was not compared against its acceptance data")
	}

	record.Classification, record.Notes = classifySurveyRecord(record, notes)
	utxosurvey.Write(record)
}

// recomputeMultisetFromActualSet builds a fresh multiset over the absolute UTXO set implied by diff
// - virtual's materialised UTXO table combined with diff - the same construction
// verifyMultisetSelfConsistency uses, and the one that answers "what does this node's UTXO set
// actually hash to", as opposed to what its incrementally maintained multiset claims.
func (csm *consensusStateManager) recomputeMultisetFromActualSet(stagingArea *model.StagingArea,
	diff externalapi.UTXODiff,
) (*externalapi.DomainHash, error) {
	virtualIterator, err := csm.consensusStateStore.VirtualUTXOSetIterator(csm.databaseContext, stagingArea)
	if err != nil {
		return nil, err
	}
	defer virtualIterator.Close()

	iterator, err := utxo.IteratorWithDiff(virtualIterator, diff)
	if err != nil {
		return nil, err
	}
	defer iterator.Close()

	fresh := multiset.New()
	for ok := iterator.First(); ok; ok = iterator.Next() {
		outpoint, entry, err := iterator.Get()
		if err != nil {
			return nil, err
		}
		serialized, err := utxo.SerializeUTXO(entry, outpoint)
		if err != nil {
			return nil, err
		}
		fresh.Add(serialized)
	}
	return fresh.Hash(), nil
}

// acceptedOutput is one UTXO this block's acceptance data says was created, i.e. what the header's
// AcceptedIDMerkleRoot commits to having happened.
type acceptedOutput struct {
	output     *externalapi.DomainTransactionOutput
	isCoinbase bool
}

// summarizeAcceptance fills the record's acceptance fields and returns every outpoint the block's
// acceptance data creates, which is what distinguishes a coin this block should have made
// (NEW_MISSING) from one that should already have existed (ORIGINAL_MISSING).
func summarizeAcceptance(record *utxosurvey.Record,
	acceptanceData externalapi.AcceptanceData,
) map[externalapi.DomainOutpoint]acceptedOutput {
	created := make(map[externalapi.DomainOutpoint]acceptedOutput)
	maxTxIDs := utxosurvey.MaxTxIDs()

	appendCapped := func(list []string, truncated *int, id string) []string {
		if maxTxIDs != 0 && len(list) >= maxTxIDs {
			*truncated++
			return list
		}
		return append(list, id)
	}

	for _, blockAcceptanceData := range acceptanceData {
		for i, transactionAcceptance := range blockAcceptanceData.TransactionAcceptanceData {
			transactionID := consensushashing.TransactionID(transactionAcceptance.Transaction)
			if !transactionAcceptance.IsAccepted {
				record.RejectedOrRedTxIDs = appendCapped(record.RejectedOrRedTxIDs,
					&record.RejectedOrRedTxIDsTruncated, transactionID.String())
				continue
			}
			record.AcceptanceTxCount++
			record.AcceptedTxIDs = appendCapped(record.AcceptedTxIDs,
				&record.AcceptedTxIDsTruncated, transactionID.String())

			isCoinbase := i == 0
			for outputIndex, output := range transactionAcceptance.Transaction.Outputs {
				outpoint := externalapi.DomainOutpoint{TransactionID: *transactionID, Index: uint32(outputIndex)}
				created[outpoint] = acceptedOutput{output: output, isCoinbase: isCoinbase}
			}
		}
	}
	return created
}

// surveyMissingOutpoints answers, for each outpoint the block could not resolve, the question the
// whole survey exists for: is the coin gone, or is it here under different bytes? Every lookup is a
// point lookup, so this stays cheap enough to run on every failing block.
func (csm *consensusStateManager) surveyMissingOutpoints(stagingArea *model.StagingArea, survey *blockSurvey,
	blockDAAScore uint64, selectedParentPastUTXO, pastUTXODiff externalapi.UTXODiff,
	createdByAcceptance map[externalapi.DomainOutpoint]acceptedOutput,
) []utxosurvey.MissingOutpoint {
	if len(survey.missingOrder) == 0 {
		return nil
	}

	results := make([]utxosurvey.MissingOutpoint, 0, len(survey.missingOrder))
	for _, outpointKey := range survey.missingOrder {
		outpoint := outpointKey
		missing := survey.missing[outpointKey]

		result := utxosurvey.MissingOutpoint{
			TxID:      outpoint.TransactionID.String(),
			Index:     outpoint.Index,
			SpentByTx: missing.spentBy,
		}

		// What the block's own acceptance data says about this outpoint. If it creates it, the entry
		// it would create carries the merging block's DAA score - utxo.AcceptedUTXOBlockDAAScore - and
		// that is the preimage every other copy of this coin has to match.
		var expectedEntry externalapi.UTXOEntry
		if created, ok := createdByAcceptance[outpoint]; ok {
			result.FoundInMergesetAdds = true
			expectedDAAScore := utxo.AcceptedUTXOBlockDAAScore(blockDAAScore)
			result.ExpectedBlockDAAScore = &expectedDAAScore
			expectedEntry = utxo.NewUTXOEntry(created.output.Value, created.output.ScriptPublicKey,
				created.isCoinbase, expectedDAAScore)
			result.AlternateMatches = appendAlternateMatch(result.AlternateMatches,
				utxosurvey.SourceMergesetAcceptance, &outpoint, expectedEntry)
		}

		// Every other place this node could be holding the coin. Collecting all of them, rather than
		// stopping at the first, is what makes a byte-level comparison possible.
		if pastUTXODiff != nil {
			if entry, ok := pastUTXODiff.ToAdd().Get(&outpoint); ok {
				result.AlternateMatches = appendAlternateMatch(result.AlternateMatches,
					utxosurvey.SourcePastDiffToAdd, &outpoint, entry)
			}
			if entry, ok := pastUTXODiff.ToRemove().Get(&outpoint); ok {
				// The outpoint is absent because this block's own past already spent it. That is
				// correct behaviour for a double spend, not a lost coin, and must not be counted as one.
				result.AlreadySpentInThisPast = true
				result.AlternateMatches = appendAlternateMatch(result.AlternateMatches,
					utxosurvey.SourcePastDiffToRemove, &outpoint, entry)
			}
		}

		virtualEntry, foundInVirtual := csm.virtualUTXOEntry(stagingArea, &outpoint)
		if foundInVirtual {
			result.AlternateMatches = appendAlternateMatch(result.AlternateMatches,
				utxosurvey.SourceVirtualUTXOSet, &outpoint, virtualEntry)
		}

		// foundInParentSet is the selected parent's own UTXO view - virtual's table as amended by the
		// parent's accumulated diff - which is the set this block was actually validated against. With
		// no parent diff available it degrades to virtual alone; the record's notes say so.
		result.FoundInParentSet = foundInVirtual
		if selectedParentPastUTXO != nil {
			if entry, ok := selectedParentPastUTXO.ToAdd().Get(&outpoint); ok {
				result.FoundInParentSet = true
				result.AlternateMatches = appendAlternateMatch(result.AlternateMatches,
					"selected-parent-diff-toAdd", &outpoint, entry)
			} else if selectedParentPastUTXO.ToRemove().Contains(&outpoint) {
				result.FoundInParentSet = false
			}
		}

		// The handling-versus-loss verdict for this one outpoint: does any copy of the coin differ
		// from the reference preimage in exactly the fields SerializeUTXO commits to?
		reference := expectedEntry
		if reference == nil {
			reference = virtualEntry
		}
		for _, match := range result.AlternateMatches {
			if match.Source == utxosurvey.SourceMergesetAcceptance || reference == nil {
				continue
			}
			if match.BlockDAAScore != reference.BlockDAAScore() {
				result.FoundUnderDifferentDAAScore = true
			}
			if match.Amount != reference.Amount() ||
				match.ScriptVersion != reference.ScriptPublicKey().Version ||
				match.ScriptPublicKey != hex.EncodeToString(reference.ScriptPublicKey().Script) ||
				match.IsCoinbase != reference.IsCoinbase() {
				result.FoundUnderDifferentAmountOrScript = true
			}
		}

		results = append(results, result)
	}
	return results
}

// virtualUTXOEntry is a point lookup into virtual's materialised UTXO table, reporting absence
// rather than an error so the survey can record "not here" as a finding.
func (csm *consensusStateManager) virtualUTXOEntry(stagingArea *model.StagingArea,
	outpoint *externalapi.DomainOutpoint,
) (externalapi.UTXOEntry, bool) {
	hasEntry, err := csm.consensusStateStore.HasUTXOByOutpoint(csm.databaseContext, stagingArea, outpoint)
	if err != nil || !hasEntry {
		return nil, false
	}
	entry, _, err := csm.consensusStateStore.UTXOByOutpoint(csm.databaseContext, stagingArea, outpoint)
	if err != nil {
		return nil, false
	}
	return entry, true
}

// appendAlternateMatch records one copy of a coin together with the exact bytes that copy would
// contribute to a MuHash. Two matches whose SerializedUTXO differ prove the disagreement is about
// the coin's identity, not its existence.
func appendAlternateMatch(matches []utxosurvey.AlternateMatch, source string,
	outpoint *externalapi.DomainOutpoint, entry externalapi.UTXOEntry,
) []utxosurvey.AlternateMatch {
	if entry == nil {
		return matches
	}
	match := utxosurvey.AlternateMatch{
		Source:          source,
		Amount:          entry.Amount(),
		ScriptVersion:   entry.ScriptPublicKey().Version,
		ScriptPublicKey: hex.EncodeToString(entry.ScriptPublicKey().Script),
		IsCoinbase:      entry.IsCoinbase(),
		BlockDAAScore:   entry.BlockDAAScore(),
	}
	if serialized, err := utxo.SerializeUTXO(entry, outpoint); err == nil {
		match.SerializedUTXO = hex.EncodeToString(serialized)
	}
	return append(matches, match)
}

// surveyBlockDelta compares the block's own UTXO delta - the difference between its past UTXO set
// and its selected parent's, which is what the diff chain will persist - against the delta its
// acceptance data describes, which is what the header's AcceptedIDMerkleRoot commits to. They are
// built by two separate implementations (MutableUTXODiff.AddTransaction versus a direct read of
// acceptance data) and are meant to be two spellings of one set, so every disagreement is an
// element the commitment will be wrong by.
//
// Both directions are reported, distinguished by the elements' Reason:
// an element the diff has and acceptance does not, and an element acceptance has and the diff does
// not.
func surveyBlockDelta(selectedParentPastUTXO, pastUTXODiff externalapi.UTXODiff,
	acceptanceData externalapi.AcceptanceData, blockDAAScore uint64,
) (extraAdds, extraRemoves []utxosurvey.DiffElement, err error) {
	delta, err := selectedParentPastUTXO.DiffFrom(pastUTXODiff)
	if err != nil {
		return nil, nil, err
	}

	expectedAdds := make(map[externalapi.DomainOutpoint]externalapi.UTXOEntry)
	expectedRemoves := make(map[externalapi.DomainOutpoint]struct{})
	for _, blockAcceptanceData := range acceptanceData {
		for i, transactionAcceptance := range blockAcceptanceData.TransactionAcceptanceData {
			if !transactionAcceptance.IsAccepted {
				continue
			}
			transaction := transactionAcceptance.Transaction
			transactionID := consensushashing.TransactionID(transaction)
			isCoinbase := i == 0
			for outputIndex, output := range transaction.Outputs {
				outpoint := externalapi.DomainOutpoint{TransactionID: *transactionID, Index: uint32(outputIndex)}
				expectedAdds[outpoint] = utxo.NewUTXOEntry(output.Value, output.ScriptPublicKey, isCoinbase,
					utxo.AcceptedUTXOBlockDAAScore(blockDAAScore))
			}
			for _, input := range transaction.Inputs {
				expectedRemoves[input.PreviousOutpoint] = struct{}{}
			}
		}
	}

	// A coin this block both creates and spends nets to no element in the delta at all, so it is
	// correctly absent from toAdd and from toRemove alike. Both leftover loops below would otherwise
	// report it twice over - once as an accepted output that never reached the diff, which reads as a
	// newly-missing coin, and once as an accepted input the diff never removed. Computed here, before
	// either loop consumes its map.
	createdAndSpentHere := make(map[externalapi.DomainOutpoint]struct{})
	for outpoint := range expectedRemoves {
		if _, created := expectedAdds[outpoint]; created {
			createdAndSpentHere[outpoint] = struct{}{}
		}
	}

	maxElements := utxosurvey.MaxTxIDs()
	appendElement := func(list []utxosurvey.DiffElement, outpoint *externalapi.DomainOutpoint,
		entry externalapi.UTXOEntry, reason string,
	) []utxosurvey.DiffElement {
		if maxElements != 0 && len(list) >= maxElements {
			return list
		}
		element := utxosurvey.DiffElement{
			TxID:   outpoint.TransactionID.String(),
			Index:  outpoint.Index,
			Reason: reason,
		}
		if entry != nil {
			element.Amount = entry.Amount()
			element.IsCoinbase = entry.IsCoinbase()
			element.BlockDAAScore = entry.BlockDAAScore()
			if serialized, serializeErr := utxo.SerializeUTXO(entry, outpoint); serializeErr == nil {
				element.SerializedUTXO = hex.EncodeToString(serialized)
			}
		}
		return append(list, element)
	}

	addIterator := delta.ToAdd().Iterator()
	defer addIterator.Close()
	for ok := addIterator.First(); ok; ok = addIterator.Next() {
		outpoint, entry, iteratorErr := addIterator.Get()
		if iteratorErr != nil {
			return nil, nil, iteratorErr
		}
		expected, isExpected := expectedAdds[*outpoint]
		switch {
		case !isExpected:
			extraAdds = appendElement(extraAdds, outpoint, entry, "add-not-in-acceptance-data")
		case !entry.Equal(expected):
			extraAdds = appendElement(extraAdds, outpoint, entry, "add-differs-from-acceptance-data")
		}
		delete(expectedAdds, *outpoint)
	}
	for outpoint, entry := range expectedAdds {
		if _, netsToNothing := createdAndSpentHere[outpoint]; netsToNothing {
			continue
		}
		outpointCopy := outpoint
		extraAdds = appendElement(extraAdds, &outpointCopy, entry, "acceptance-output-absent-from-diff")
	}

	removeIterator := delta.ToRemove().Iterator()
	defer removeIterator.Close()
	for ok := removeIterator.First(); ok; ok = removeIterator.Next() {
		outpoint, entry, iteratorErr := removeIterator.Get()
		if iteratorErr != nil {
			return nil, nil, iteratorErr
		}
		if _, isExpected := expectedRemoves[*outpoint]; !isExpected {
			extraRemoves = appendElement(extraRemoves, outpoint, entry, "remove-not-in-acceptance-data")
		}
		delete(expectedRemoves, *outpoint)
	}
	for outpoint := range expectedRemoves {
		if _, netsToNothing := createdAndSpentHere[outpoint]; netsToNothing {
			continue
		}
		outpointCopy := outpoint
		extraRemoves = appendElement(extraRemoves, &outpointCopy, nil, "acceptance-input-absent-from-diff")
	}

	return extraAdds, extraRemoves, nil
}

// classifySurveyRecord places one record in the A/B/C table, most specific verdict first, and
// explains the verdict in the record's notes so a cluster can be read without re-deriving it.
//
// The order matters. A coin that is present under different identity bytes is not lost however many
// other symptoms accompany it, so HANDLING_MISMATCH outranks both missing verdicts; and a coin this
// block itself was supposed to create being absent (NEW_MISSING) is a different bug from a coin
// that should have arrived with the pruning point (ORIGINAL_MISSING), so the newly-created case is
// checked first.
func classifySurveyRecord(record *utxosurvey.Record, notes []string) (classification, joinedNotes string) {
	var handlingMismatch, newMissing, originalMissing, onlyAlreadySpent bool
	onlyAlreadySpent = len(record.MissingOutpoints) > 0

	for _, missing := range record.MissingOutpoints {
		if !missing.AlreadySpentInThisPast {
			onlyAlreadySpent = false
		}
		if missing.FoundUnderDifferentDAAScore || missing.FoundUnderDifferentAmountOrScript {
			handlingMismatch = true
			continue
		}
		if missing.AlreadySpentInThisPast {
			continue
		}
		if missing.FoundInMergesetAdds {
			newMissing = true
			continue
		}
		if !missing.FoundInParentSet {
			originalMissing = true
		}
	}

	for _, extra := range record.ExtraAddsNotInHeaderView {
		if extra.Reason == "add-differs-from-acceptance-data" {
			handlingMismatch = true
		}
		if extra.Reason == "acceptance-output-absent-from-diff" {
			newMissing = true
		}
	}

	switch {
	case handlingMismatch:
		classification = utxosurvey.ClassificationHandlingMismatch
		notes = append(notes, "a copy of at least one outpoint exists with a different SerializeUTXO "+
			"preimage - the coin is present and the disagreement is about its identity bytes, not its existence")
	case newMissing:
		classification = utxosurvey.ClassificationNewMissing
		notes = append(notes, "at least one absent outpoint is created by this block's own acceptance data")
	case originalMissing:
		classification = utxosurvey.ClassificationOriginalMissing
		notes = append(notes, "at least one absent outpoint is in neither the selected parent's UTXO view "+
			"nor anything this block accepts - it should have arrived with the pruning point UTXO set")
	case onlyAlreadySpent:
		classification = utxosurvey.ClassificationUnknown
		notes = append(notes, "every absent outpoint was already spent in this block's own past - correct "+
			"behaviour for a double spend, not a lost coin")
	case len(record.MissingOutpoints) == 0 && record.HeaderUTXOCommitment != record.CalculatedUTXOCommitment:
		classification = utxosurvey.ClassificationCommitmentOnly
		if len(record.ExtraAddsNotInHeaderView) > 0 || len(record.ExtraRemovesNotInHeaderView) > 0 {
			notes = append(notes, "no spend failed, but this block's delta and its acceptance data disagree "+
				"on specific elements - see extraAdds/extraRemoves")
			break
		}
		// Nothing about this block's own arithmetic is wrong, so the offset came from somewhere. The
		// selected parent's two representations say where: a parent whose stored multiset already
		// disagrees with its own header is the offset's source, and this block merely carries it
		// forward (MuHash is homomorphic, so it carries forward exactly). A parent that agrees with
		// its header means the offset appears here, which is the one worth chasing.
		parentIsOffset := record.ParentStoredMultiset != "" && record.ParentHeaderUTXOCommitment != "" &&
			record.ParentStoredMultiset != record.ParentHeaderUTXOCommitment
		if parentIsOffset {
			notes = append(notes, "no spend failed and this block's own delta matches its acceptance data; "+
				"the selected parent's stored multiset already disagrees with its own header, so this block "+
				"is carrying an inherited offset rather than creating one")
		} else {
			notes = append(notes, "no spend failed and this block's own delta matches its acceptance data, "+
				"yet the commitment is wrong and the selected parent's multiset agrees with its own header - "+
				"the offset appears at this block")
		}
	default:
		classification = utxosurvey.ClassificationUnknown
	}

	return classification, strings.Join(notes, "; ")
}

// recordPruningPointImportSurvey writes a record for the pruning-point UTXO import itself. Its
// failure is upstream of every per-block failure that follows - if the imported set does not hash
// to the pruning point's own header commitment, every block resolved forward inherits that offset -
// so it has to be in the same file as those blocks, at stage pruning-utxo-import, to be clustered
// against them.
func (csm *consensusStateManager) recordPruningPointImportSurvey(stagingArea *model.StagingArea,
	pruningPoint *externalapi.DomainHash, importedMultiset model.Multiset, errorLabel, notes string,
	missingOutpoints []*externalapi.DomainOutpoint, spentBy map[externalapi.DomainOutpoint]string,
) {
	if !utxosurvey.Enabled() {
		return
	}

	record := &utxosurvey.Record{
		BlockHash:      pruningPoint.String(),
		IBDStage:       utxosurvey.StagePruningUTXOImport,
		Error:          errorLabel,
		IsChainBlock:   true,
		Classification: utxosurvey.ClassificationUnknown,
		Notes:          notes,
	}
	if importedMultiset != nil {
		record.CalculatedUTXOCommitment = importedMultiset.Hash().String()
	}
	if header, err := csm.blockHeaderStore.BlockHeader(csm.databaseContext, stagingArea, pruningPoint); err == nil {
		record.HeaderUTXOCommitment = header.UTXOCommitment().String()
		record.DAAScore = header.DAAScore()
	}
	if block, err := csm.blockStore.Block(csm.databaseContext, stagingArea, pruningPoint); err == nil &&
		len(block.Transactions) > 0 {
		record.CoinbaseTxID = consensushashing.TransactionID(block.Transactions[0]).String()
	}

	// The imported set is the only place these could have come from, so anything absent from it is
	// original by construction - the snapshot or its transfer dropped it. Looked up in one pass: the
	// bucket has no point-lookup API and holds millions of entries, so a scan per outpoint would turn
	// a handful of missing coins into a handful of full scans in the middle of an import.
	importedEntries := csm.importedPruningPointUTXOEntries(missingOutpoints)
	for _, outpoint := range missingOutpoints {
		if outpoint == nil {
			continue
		}
		missing := utxosurvey.MissingOutpoint{
			TxID:      outpoint.TransactionID.String(),
			Index:     outpoint.Index,
			SpentByTx: spentBy[*outpoint],
		}
		if entry, found := importedEntries[*outpoint]; found {
			missing.FoundInParentSet = true
			missing.AlternateMatches = appendAlternateMatch(missing.AlternateMatches,
				utxosurvey.SourceImportedPruningSet, outpoint, entry)
		}
		record.MissingOutpoints = append(record.MissingOutpoints, missing)
	}

	if len(record.MissingOutpoints) > 0 {
		record.Classification = utxosurvey.ClassificationOriginalMissing
	} else if record.HeaderUTXOCommitment != "" && record.HeaderUTXOCommitment != record.CalculatedUTXOCommitment {
		record.Classification = utxosurvey.ClassificationCommitmentOnly
	}

	utxosurvey.Write(record)
}

// importedPruningPointUTXOEntries looks the given outpoints up in the imported pruning point UTXO
// bucket in a single pass, returning only those it holds. The bucket has no point-lookup API - its
// cursor seeks to the first key at or after the one asked for, which cannot distinguish "here" from
// "not here" - so it has to be walked, and walking it once for the whole set is the difference
// between one scan and one scan per missing coin. Stops early once every target has been found.
func (csm *consensusStateManager) importedPruningPointUTXOEntries(targets []*externalapi.DomainOutpoint) map[externalapi.DomainOutpoint]externalapi.UTXOEntry {
	found := make(map[externalapi.DomainOutpoint]externalapi.UTXOEntry)
	wanted := make(map[externalapi.DomainOutpoint]struct{}, len(targets))
	for _, target := range targets {
		if target != nil {
			wanted[*target] = struct{}{}
		}
	}
	if len(wanted) == 0 {
		return found
	}

	iterator, err := csm.pruningStore.ImportedPruningPointUTXOIterator(csm.databaseContext)
	if err != nil {
		log.Debugf("UTXO survey: cannot iterate the imported pruning point UTXO set: %s", err)
		return found
	}
	defer iterator.Close()

	for ok := iterator.First(); ok; ok = iterator.Next() {
		outpoint, entry, err := iterator.Get()
		if err != nil {
			log.Debugf("UTXO survey: iterating the imported pruning point UTXO set failed: %s", err)
			return found
		}
		if _, isWanted := wanted[*outpoint]; !isWanted {
			continue
		}
		found[*outpoint] = entry
		if len(found) == len(wanted) {
			break
		}
	}
	return found
}

// surveyCascadedBlock verifies a block that ResolveBlockStatus is about to disqualify purely by
// inheritance, and records what it would have failed on. It is a no-op unless the survey is on.
//
// The verdict is deliberately thrown away: the block's status is decided by its selected parent's,
// exactly as before, and verifyUTXO only reads (blockStore hands out a clone, so the fee and
// UTXO-entry population it does affects nothing outside this call). Without this, a disqualification
// cascade contributes exactly one record - its root - however many blocks it swallows, which is the
// specific blindness this survey was built to remove.
func (csm *consensusStateManager) surveyCascadedBlock(stagingArea *model.StagingArea,
	blockHash, selectedParentHash *externalapi.DomainHash,
	selectedParentPastUTXO, pastUTXODiff externalapi.UTXODiff, acceptanceData externalapi.AcceptanceData,
	blockMultiset model.Multiset,
) {
	survey := newBlockSurvey(utxosurvey.StageChainReplay)
	if !survey.active() {
		return
	}
	block, err := csm.blockStore.Block(csm.databaseContext, stagingArea, blockHash)
	if err != nil {
		log.Debugf("UTXO survey: cannot read cascaded block %s to verify it: %s", blockHash, err)
		return
	}

	_ = csm.verifyUTXO(stagingArea, block, blockHash, pastUTXODiff, acceptanceData, blockMultiset, survey)
	csm.recordBlockSurvey(stagingArea, survey, block, blockHash, selectedParentHash,
		selectedParentPastUTXO, pastUTXODiff, acceptanceData, blockMultiset)
}
