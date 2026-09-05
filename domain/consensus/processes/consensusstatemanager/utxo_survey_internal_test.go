package consensusstatemanager

import (
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/ruleerrors"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/utxo"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/utxosurvey"
	"github.com/pkg/errors"
)

func outpoint(marker byte, index uint32) *externalapi.DomainOutpoint {
	var transactionID externalapi.DomainTransactionID
	idBytes := [externalapi.DomainHashSize]byte{}
	idBytes[0] = marker
	transactionID = externalapi.DomainTransactionID(*externalapi.NewDomainHashFromByteArray(&idBytes))
	return &externalapi.DomainOutpoint{TransactionID: transactionID, Index: index}
}

// TestSurveyErrorLabelDoesNotPanicOnMissingTxOut is the reason surveyErrorLabel reads the rule name
// out of the error string instead of using errors.Is. RuleError is only statically comparable - its
// inner field is an interface - so comparing one carrying an ErrMissingTxOut (whose MissingOutpoints
// is a slice) against a sentinel panics. The label has to be derivable from the error that occurs
// most often in this survey, so this pins that it is.
func TestSurveyErrorLabelDoesNotPanicOnMissingTxOut(t *testing.T) {
	missingTxOutError := ruleerrors.NewErrMissingTxOut([]*externalapi.DomainOutpoint{outpoint(1, 0)})

	// The comparison surveyErrorLabel deliberately avoids. If this ever stops panicking, the comment
	// in surveyErrorLabel is stale, not wrong to have been cautious about.
	func() {
		defer func() {
			if recover() == nil {
				t.Log("errors.Is against a rule-error sentinel no longer panics on ErrMissingTxOut; " +
					"surveyErrorLabel's string-based approach is now merely unnecessary, not required")
			}
		}()
		_ = errors.Is(missingTxOutError, ruleerrors.ErrBadUTXOCommitment)
	}()

	if label := surveyErrorLabel("block-transactions-vs-past-utxo", missingTxOutError); label != "missing-input" {
		t.Errorf("expected a missing-input label, got %q", label)
	}
}

func TestSurveyErrorLabel(t *testing.T) {
	tests := []struct {
		name     string
		step     string
		err      error
		expected string
	}{{
		name:     "wrapped rule error keeps its rule name",
		step:     "utxo-commitment",
		err:      errors.Wrapf(ruleerrors.ErrBadUTXOCommitment, "block %s is invalid", "abc"),
		expected: "ErrBadUTXOCommitment",
	}, {
		name:     "missing outputs are labelled by kind, not by step",
		step:     "block-transactions-vs-past-utxo",
		err:      ruleerrors.NewErrMissingTxOut([]*externalapi.DomainOutpoint{outpoint(2, 1)}),
		expected: "missing-input",
	}, {
		name:     "a non-rule error falls back to the step that produced it",
		step:     "coinbase-transaction",
		err:      errors.New("database is on fire"),
		expected: "coinbase-transaction",
	}}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if label := surveyErrorLabel(test.step, test.err); label != test.expected {
				t.Errorf("expected %q, got %q", test.expected, label)
			}
		})
	}
}

// TestBlockSurveyKeepsEveryFailure is the Phase 1 requirement in miniature: one block that fails
// several checks and cannot resolve several inputs has to produce one record naming all of them.
func TestBlockSurveyKeepsEveryFailure(t *testing.T) {
	t.Setenv("HTND_UTXO_SURVEY", "/dev/null")
	utxosurvey.Reset()
	t.Cleanup(utxosurvey.Reset)

	survey := newBlockSurvey(utxosurvey.StageChainReplay)
	if !survey.active() {
		t.Fatal("survey should be active with HTND_UTXO_SURVEY set")
	}

	survey.noteFailure("utxo-commitment", errors.Wrap(ruleerrors.ErrBadUTXOCommitment, "first symptom"))
	survey.noteFailure("coinbase-transaction", errors.Wrap(ruleerrors.ErrBadCoinbaseTransaction, "second symptom"))

	firstMissing := ruleerrors.NewErrMissingTxOut([]*externalapi.DomainOutpoint{outpoint(3, 0), outpoint(4, 1)})
	secondMissing := ruleerrors.NewErrMissingTxOut([]*externalapi.DomainOutpoint{outpoint(5, 2)})
	survey.noteFailure("block-transactions-vs-past-utxo", firstMissing)
	survey.noteMissingOutpointsFromError("tx-one", firstMissing)
	survey.noteMissingOutpointsFromError("tx-two", secondMissing)
	// The same outpoint reported twice is one finding, not two.
	survey.noteMissingOutpointsFromError("tx-three", secondMissing)

	if !survey.failed() {
		t.Fatal("survey recorded nothing")
	}
	if len(survey.missingOrder) != 3 {
		t.Fatalf("expected 3 distinct missing outpoints, got %d", len(survey.missingOrder))
	}
	if spentBy := survey.missing[*outpoint(5, 2)].spentBy; spentBy != "tx-two" {
		t.Errorf("expected the first spender to be attributed, got %q", spentBy)
	}

	label := survey.errorLabel()
	expected := "ErrBadUTXOCommitment+ErrBadCoinbaseTransaction+missing-input"
	if label != expected {
		t.Errorf("expected every failed check in the label\n  want: %s\n  got:  %s", expected, label)
	}
}

// TestBlockSurveyIsInertWhenDisabled pins that all of this costs nothing, and crashes nothing, when
// the survey is off - which is how it ships.
func TestBlockSurveyIsInertWhenDisabled(t *testing.T) {
	t.Setenv("HTND_UTXO_SURVEY", "")
	utxosurvey.Reset()
	t.Cleanup(utxosurvey.Reset)

	survey := newBlockSurvey(utxosurvey.StageChainReplay)
	if survey != nil {
		t.Fatal("expected no survey when HTND_UTXO_SURVEY is unset")
	}
	// Every method has to be safe on the nil survey, because the call sites do not guard.
	survey.noteFailure("utxo-commitment", errors.New("boom"))
	survey.noteMissingOutpointsFromError("tx", ruleerrors.NewErrMissingTxOut(
		[]*externalapi.DomainOutpoint{outpoint(6, 0)}))
	if survey.active() || survey.failed() {
		t.Fatal("a nil survey should report itself inactive and failure-free")
	}
}

func TestClassifySurveyRecord(t *testing.T) {
	daaScore := uint64(1000)

	tests := []struct {
		name     string
		record   utxosurvey.Record
		expected string
	}{{
		name: "a coin present under a different DAA score is a handling mismatch, not a loss",
		record: utxosurvey.Record{
			MissingOutpoints: []utxosurvey.MissingOutpoint{{
				FoundInParentSet:            true,
				FoundUnderDifferentDAAScore: true,
			}},
		},
		expected: utxosurvey.ClassificationHandlingMismatch,
	}, {
		name: "a handling mismatch outranks a missing coin in the same block",
		record: utxosurvey.Record{
			MissingOutpoints: []utxosurvey.MissingOutpoint{
				{FoundUnderDifferentAmountOrScript: true},
				{FoundInParentSet: false},
			},
		},
		expected: utxosurvey.ClassificationHandlingMismatch,
	}, {
		name: "a coin this block's own acceptance data creates is newly missing",
		record: utxosurvey.Record{
			MissingOutpoints: []utxosurvey.MissingOutpoint{{
				FoundInMergesetAdds:   true,
				ExpectedBlockDAAScore: &daaScore,
			}},
		},
		expected: utxosurvey.ClassificationNewMissing,
	}, {
		name: "an accepted output absent from the block's own diff is newly missing",
		record: utxosurvey.Record{
			ExtraAddsNotInHeaderView: []utxosurvey.DiffElement{{
				Reason: "acceptance-output-absent-from-diff",
			}},
		},
		expected: utxosurvey.ClassificationNewMissing,
	}, {
		name: "a coin in neither the parent's view nor this block's acceptance is original",
		record: utxosurvey.Record{
			MissingOutpoints: []utxosurvey.MissingOutpoint{{FoundInParentSet: false}},
		},
		expected: utxosurvey.ClassificationOriginalMissing,
	}, {
		name: "an outpoint this block's own past already spent is not a finding",
		record: utxosurvey.Record{
			MissingOutpoints: []utxosurvey.MissingOutpoint{{AlreadySpentInThisPast: true}},
		},
		expected: utxosurvey.ClassificationUnknown,
	}, {
		name: "a commitment mismatch with no failed spend is commitment-only",
		record: utxosurvey.Record{
			HeaderUTXOCommitment:     "aaaa",
			CalculatedUTXOCommitment: "bbbb",
		},
		expected: utxosurvey.ClassificationCommitmentOnly,
	}, {
		name: "an element the diff has and acceptance does not is still commitment-only",
		record: utxosurvey.Record{
			HeaderUTXOCommitment:     "aaaa",
			CalculatedUTXOCommitment: "bbbb",
			ExtraAddsNotInHeaderView: []utxosurvey.DiffElement{{Reason: "add-not-in-acceptance-data"}},
		},
		expected: utxosurvey.ClassificationCommitmentOnly,
	}, {
		name: "an element the diff spells differently from acceptance is a handling mismatch",
		record: utxosurvey.Record{
			HeaderUTXOCommitment:     "aaaa",
			CalculatedUTXOCommitment: "bbbb",
			ExtraAddsNotInHeaderView: []utxosurvey.DiffElement{{Reason: "add-differs-from-acceptance-data"}},
		},
		expected: utxosurvey.ClassificationHandlingMismatch,
	}, {
		name:     "a matching commitment and no missing outpoints classifies as unknown",
		record:   utxosurvey.Record{HeaderUTXOCommitment: "aaaa", CalculatedUTXOCommitment: "aaaa"},
		expected: utxosurvey.ClassificationUnknown,
	}}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			classification, notes := classifySurveyRecord(&test.record, nil)
			if classification != test.expected {
				t.Errorf("expected %s, got %s (notes: %s)", test.expected, classification, notes)
			}
		})
	}
}

// surveyDeltaFixture builds the two arguments surveyBlockDelta compares. The parent's view is empty
// so that the delta between the two is exactly blockDiff - DiffFrom on an empty diff returns the
// other diff unchanged - which keeps the fixture about the comparison rather than about diff
// arithmetic.
func surveyDeltaFixture(t *testing.T, toAdd, toRemove map[externalapi.DomainOutpoint]externalapi.UTXOEntry) (
	parentView, blockDiff externalapi.UTXODiff,
) {
	t.Helper()
	blockDiff, err := utxo.NewUTXODiffFromCollections(utxo.NewUTXOCollection(toAdd), utxo.NewUTXOCollection(toRemove))
	if err != nil {
		t.Fatalf("NewUTXODiffFromCollections: %+v", err)
	}
	return utxo.NewUTXODiff(), blockDiff
}

// acceptedTransaction builds one accepted transaction for an acceptance-data fixture. Index 0 of a
// block's acceptance data is its coinbase, which is what decides the isCoinbase flag on every entry
// the transaction creates - and isCoinbase is part of the SerializeUTXO preimage, so it has to be
// right here too.
func acceptedTransaction(inputs []*externalapi.DomainOutpoint, outputValues ...uint64,
) *externalapi.DomainTransaction {
	transaction := &externalapi.DomainTransaction{
		Version: 0,
		Inputs:  make([]*externalapi.DomainTransactionInput, 0, len(inputs)),
		Outputs: make([]*externalapi.DomainTransactionOutput, 0, len(outputValues)),
	}
	for _, input := range inputs {
		transaction.Inputs = append(transaction.Inputs, &externalapi.DomainTransactionInput{
			PreviousOutpoint: *input,
		})
	}
	for _, value := range outputValues {
		transaction.Outputs = append(transaction.Outputs, &externalapi.DomainTransactionOutput{
			Value:           value,
			ScriptPublicKey: &externalapi.ScriptPublicKey{Script: []byte{0x51}, Version: 0},
		})
	}
	return transaction
}

func acceptanceDataOf(transactions ...*externalapi.DomainTransaction) externalapi.AcceptanceData {
	transactionAcceptanceData := make([]*externalapi.TransactionAcceptanceData, 0, len(transactions))
	for _, transaction := range transactions {
		transactionAcceptanceData = append(transactionAcceptanceData, &externalapi.TransactionAcceptanceData{
			Transaction: transaction,
			IsAccepted:  true,
		})
	}
	return externalapi.AcceptanceData{{
		BlockHash:                 externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{9}),
		TransactionAcceptanceData: transactionAcceptanceData,
	}}
}

func reasonsOf(elements []utxosurvey.DiffElement) []string {
	reasons := make([]string, 0, len(elements))
	for _, element := range elements {
		reasons = append(reasons, element.Reason)
	}
	return reasons
}

// TestSurveyBlockDeltaIgnoresCoinsCreatedAndSpentInTheSameBlock is the regression this fixture
// exists for. A coin a block both creates and spends nets to nothing in that block's UTXO delta, so
// it is correctly absent from toAdd and from toRemove alike. Reporting it as an accepted output the
// diff never received would classify a perfectly healthy block as NEW_MISSING - a lost coin that
// was never lost - which is the worst thing a survey meant to find lost coins can do.
func TestSurveyBlockDeltaIgnoresCoinsCreatedAndSpentInTheSameBlock(t *testing.T) {
	const blockDAAScore = 500

	coinbase := acceptedTransaction(nil, 100)
	coinbaseID := consensushashing.TransactionID(coinbase)
	createdAndSpent := &externalapi.DomainOutpoint{TransactionID: *coinbaseID, Index: 0}
	spender := acceptedTransaction([]*externalapi.DomainOutpoint{createdAndSpent}, 90)

	// The delta holds only the spender's own output: the coinbase output it consumed was created and
	// destroyed inside this same delta and leaves no trace in it.
	spenderID := consensushashing.TransactionID(spender)
	parentView, blockDiff := surveyDeltaFixture(t,
		map[externalapi.DomainOutpoint]externalapi.UTXOEntry{
			{TransactionID: *spenderID, Index: 0}: utxo.NewUTXOEntry(90,
				&externalapi.ScriptPublicKey{Script: []byte{0x51}, Version: 0}, false, blockDAAScore),
		},
		nil)

	extraAdds, extraRemoves, err := surveyBlockDelta(parentView, blockDiff,
		acceptanceDataOf(coinbase, spender), blockDAAScore)
	if err != nil {
		t.Fatalf("surveyBlockDelta: %+v", err)
	}
	if len(extraAdds) != 0 || len(extraRemoves) != 0 {
		t.Errorf("a coin created and spent in the same block is not a finding, but the delta comparison "+
			"reported adds=%v removes=%v", reasonsOf(extraAdds), reasonsOf(extraRemoves))
	}
}

func TestSurveyBlockDelta(t *testing.T) {
	const blockDAAScore = 500
	script := &externalapi.ScriptPublicKey{Script: []byte{0x51}, Version: 0}

	coinbase := acceptedTransaction(nil, 100)
	coinbaseID := consensushashing.TransactionID(coinbase)
	coinbaseOutpoint := externalapi.DomainOutpoint{TransactionID: *coinbaseID, Index: 0}
	coinbaseEntry := utxo.NewUTXOEntry(100, script, true, blockDAAScore)

	tests := []struct {
		name            string
		toAdd           map[externalapi.DomainOutpoint]externalapi.UTXOEntry
		expectedAdds    []string
		expectedRemoves []string
	}{{
		name:  "a delta that matches its acceptance data is not a finding",
		toAdd: map[externalapi.DomainOutpoint]externalapi.UTXOEntry{coinbaseOutpoint: coinbaseEntry},
	}, {
		name:         "an accepted output the delta never received is a newly missing coin",
		toAdd:        nil,
		expectedAdds: []string{"acceptance-output-absent-from-diff"},
	}, {
		name: "an output the delta spells differently is a handling mismatch",
		toAdd: map[externalapi.DomainOutpoint]externalapi.UTXOEntry{
			// Same coin, stamped with a DAA score that is not the merging block's - the exact
			// stamping mistake the survey exists to distinguish from a loss.
			coinbaseOutpoint: utxo.NewUTXOEntry(100, script, true, blockDAAScore-1),
		},
		expectedAdds: []string{"add-differs-from-acceptance-data"},
	}, {
		name: "an add nothing accepted creates is an extra element",
		toAdd: map[externalapi.DomainOutpoint]externalapi.UTXOEntry{
			coinbaseOutpoint: coinbaseEntry,
			*outpoint(7, 0):  utxo.NewUTXOEntry(1, script, false, blockDAAScore),
		},
		expectedAdds: []string{"add-not-in-acceptance-data"},
	}}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parentView, blockDiff := surveyDeltaFixture(t, test.toAdd, nil)
			extraAdds, extraRemoves, err := surveyBlockDelta(parentView, blockDiff,
				acceptanceDataOf(coinbase), blockDAAScore)
			if err != nil {
				t.Fatalf("surveyBlockDelta: %+v", err)
			}
			if got := reasonsOf(extraAdds); !equalStrings(got, test.expectedAdds) {
				t.Errorf("extra adds: expected %v, got %v", test.expectedAdds, got)
			}
			if got := reasonsOf(extraRemoves); !equalStrings(got, test.expectedRemoves) {
				t.Errorf("extra removes: expected %v, got %v", test.expectedRemoves, got)
			}
		})
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
