package utxosurvey

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSummarizeEmptyRunSaysSoWithoutClaimingCleanliness(t *testing.T) {
	summary := Summarize(nil)
	if summary.Records != 0 {
		t.Fatalf("expected no records, got %d", summary.Records)
	}
	// A survey that was never switched on and a sync in which nothing failed produce the same empty
	// file. Reporting the second when it was the first would end the investigation on a false result.
	rendered := summary.String()
	if !strings.Contains(rendered, "HTND_UTXO_SURVEY") {
		t.Errorf("an empty summary must warn that it may mean the survey was never enabled, got:\n%s", rendered)
	}
}

// TestSummarizeSeparatesInheritedFromCreated is the question the whole clustering pass exists to
// answer: a run of blocks all carrying one offset is one finding, and the single block where the
// offset appears is the one worth chasing.
func TestSummarizeSeparatesInheritedFromCreated(t *testing.T) {
	records := []Record{{
		BlockHash:                  "origin",
		IBDStage:                   StageChainReplay,
		DAAScore:                   1000,
		Error:                      "ErrBadUTXOCommitment",
		Classification:             ClassificationCommitmentOnly,
		HeaderUTXOCommitment:       "header-origin",
		CalculatedUTXOCommitment:   "calculated-origin",
		ParentStoredMultiset:       "agrees",
		ParentHeaderUTXOCommitment: "agrees",
	}, {
		BlockHash:                  "carrier",
		IBDStage:                   StageChainReplay,
		DAAScore:                   1001,
		Error:                      "ErrBadUTXOCommitment",
		Classification:             ClassificationCommitmentOnly,
		ParentStoredMultiset:       "offset",
		ParentHeaderUTXOCommitment: "header",
	}}

	summary := Summarize(records)
	if len(summary.OffsetOriginBlocks) != 1 || summary.OffsetOriginBlocks[0].BlockHash != "origin" {
		t.Fatalf("expected exactly the block whose parent agrees with its own header to be named as the "+
			"offset's origin, got %+v", summary.OffsetOriginBlocks)
	}
	if summary.ByError["ErrBadUTXOCommitment"] != 2 {
		t.Errorf("expected both blocks counted under their error, got %v", summary.ByError)
	}
}

// TestSummarizeRanksACoinPoisoningManyBlocks pins the Q2 answer: many failures naming one outpoint
// are one lost coin, not many bugs, and the summary has to say so without the reader counting.
func TestSummarizeRanksACoinPoisoningManyBlocks(t *testing.T) {
	poisoned := func(blockHash string) Record {
		return Record{
			BlockHash:      blockHash,
			IBDStage:       StageChainReplay,
			Error:          "missing-input",
			Classification: ClassificationOriginalMissing,
			MissingOutpoints: []MissingOutpoint{
				{TxID: "shared", Index: 0},
				{TxID: blockHash + "-own", Index: 0},
			},
		}
	}
	summary := Summarize([]Record{poisoned("a"), poisoned("b"), poisoned("c")})

	if len(summary.RepeatedOutpoints) != 1 {
		t.Fatalf("expected exactly one repeated outpoint, got %+v", summary.RepeatedOutpoints)
	}
	repeated := summary.RepeatedOutpoints[0]
	if repeated.Outpoint != "shared:0" || repeated.Blocks != 3 {
		t.Errorf("expected shared:0 to be named as blocking 3 blocks, got %s in %d", repeated.Outpoint, repeated.Blocks)
	}
	// Found nowhere, created by nothing, and not explained away as an already-spent coin.
	if !repeated.AbsentEverywhere {
		t.Error("an outpoint with no alternate match anywhere should be reported as absent from every source")
	}
	if len(summary.AbsentEverywhere) != 4 {
		t.Errorf("expected all four outpoints to be absent everywhere, got %d", len(summary.AbsentEverywhere))
	}
}

// TestSummarizeSeparatesHandlingFromLoss is the loss-versus-spelling verdict at the level of bytes.
// One outpoint held under two different preimages is present, not lost; one held under a single
// preimage everywhere it appears is not a handling problem however many blocks tripped over it.
func TestSummarizeSeparatesHandlingFromLoss(t *testing.T) {
	summary := Summarize([]Record{{
		BlockHash: "block",
		IBDStage:  StageChainReplay,
		Error:     "missing-input",
		MissingOutpoints: []MissingOutpoint{{
			TxID:  "disagreeing",
			Index: 0,
			AlternateMatches: []AlternateMatch{
				{Source: SourceVirtualUTXOSet, SerializedUTXO: "aa00", BlockDAAScore: 10},
				{Source: SourceMergesetAcceptance, SerializedUTXO: "bb11", BlockDAAScore: 11},
			},
		}, {
			TxID:  "consistent",
			Index: 0,
			AlternateMatches: []AlternateMatch{
				{Source: SourceVirtualUTXOSet, SerializedUTXO: "cc22"},
				{Source: SourcePastDiffToAdd, SerializedUTXO: "cc22"},
			},
		}},
	}})

	if len(summary.DisagreeingPreimages) != 1 {
		t.Fatalf("expected exactly the outpoint whose copies differ, got %+v", summary.DisagreeingPreimages)
	}
	cluster := summary.DisagreeingPreimages[0]
	if cluster.Outpoint != "disagreeing:0" {
		t.Errorf("expected disagreeing:0, got %s", cluster.Outpoint)
	}
	if len(cluster.Preimages) != 2 {
		t.Errorf("expected both preimages to be reported so the difference can be read, got %v", cluster.Preimages)
	}
	// A coin found somewhere is never "absent everywhere", whatever its spelling.
	if len(summary.AbsentEverywhere) != 0 {
		t.Errorf("an outpoint with alternate matches is present, not absent: %+v", summary.AbsentEverywhere)
	}

	rendered := summary.String()
	if !strings.Contains(rendered, "aa00") || !strings.Contains(rendered, "bb11") {
		t.Errorf("the rendered summary must show the differing preimages, got:\n%s", rendered)
	}
}

// A coin in virtual's table but not in the failing block's past view is not, on its own, evidence of
// anything: it may have been spent in that past, or not yet created on that branch. Counting it as a
// lost coin would send the investigation after a bug that is not there, so it stays out of the
// missing-coin lists; the run-scope pass is what decides whether it was actually lost.
func TestSummarizeDoesNotCountCoinsAbsentOnlyFromABlocksPastView(t *testing.T) {
	summary := Summarize([]Record{{
		BlockHash:        "block",
		IBDStage:         StageChainReplay,
		Error:            "missing-input",
		MissingOutpoints: []MissingOutpoint{{TxID: "spent", Index: 0, AbsentFromBlocksPastView: true}},
	}})

	if len(summary.AbsentEverywhere) != 0 {
		t.Errorf("a coin absent only from this block's past view is not evidence of a lost coin: %+v",
			summary.AbsentEverywhere)
	}
	if len(summary.RepeatedOutpoints) != 0 {
		t.Errorf("a single block's outpoint is not repeated: %+v", summary.RepeatedOutpoints)
	}
}

func TestReadRejectsAMalformedSurvey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "survey.jsonl")
	contents := "{\"blockHash\":\"good\"}\n\n{not json}\n"
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("writing fixture: %+v", err)
	}

	// Skipping the bad line would silently undercount, and every conclusion drawn from a survey is a
	// count. Better to refuse than to answer "how many failed" with a number that is quietly short.
	_, err := Read(path)
	if err == nil {
		t.Fatal("expected Read to reject a malformed survey rather than silently skip the line")
	}
	if !strings.Contains(err.Error(), "line 3") {
		t.Errorf("the error should name the offending line, got: %v", err)
	}
}

func TestReadSkipsBlankLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "survey.jsonl")
	if err := os.WriteFile(path, []byte("{\"blockHash\":\"a\"}\n\n{\"blockHash\":\"b\"}\n"), 0o644); err != nil {
		t.Fatalf("writing fixture: %+v", err)
	}
	records, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %+v", err)
	}
	if len(records) != 2 {
		t.Fatalf("expected 2 records, got %d", len(records))
	}
}

// TestSummarizeFindsCoinsCreatedThenLost is the run-scope A-versus-B test, and the reason the pass
// exists at all. Per-block classification cannot see it: MissingOutpoint.FoundInMergesetAdds covers
// only the failing block's own mergeset, so a coin created earlier in the same sync and then dropped
// is filed ORIGINAL_MISSING and sends the investigation at the pruning-point import, when the loss
// actually happened on this node while it was syncing.
func TestSummarizeFindsCoinsCreatedThenLost(t *testing.T) {
	records := []Record{{
		BlockHash:      "creator",
		IBDStage:       StageChainReplay,
		Classification: ClassificationCommitmentOnly,
		AcceptedTxIDs:  []string{"coin-tx"},
	}, {
		// The fixture has to record at least one spend, or the run has no spend history at all and the
		// pass correctly refuses to call anything lost - see
		// TestSummarizeRefusesToCallCoinsLostWithNoSpendHistory. An unrelated spend is enough: what
		// matters is that this run was capable of recording one and did not record one for our coin.
		BlockHash:      "filler",
		IBDStage:       StageChainReplay,
		Classification: ClassificationCommitmentOnly,
		AcceptedSpends: []string{"unrelated-coin:0"},
	}, {
		BlockHash:        "spender",
		IBDStage:         StageChainReplay,
		Error:            "missing-input",
		Classification:   ClassificationOriginalMissing,
		MissingOutpoints: []MissingOutpoint{{TxID: "coin-tx", Index: 0}},
	}}

	summary := Summarize(records)
	if len(summary.CreatedThenLost) != 1 {
		t.Fatalf("expected the coin created at 'creator' and unresolvable at 'spender' to be found, got %+v",
			summary.CreatedThenLost)
	}
	lost := summary.CreatedThenLost[0]
	if lost.Outpoint != "coin-tx:0" || lost.CreatedAtBlock != "creator" || lost.AbsentAtBlock != "spender" {
		t.Errorf("the finding must name the coin, where it was created and where it went: %+v", lost)
	}
	if summary.CreatedThenSpentThenAbsent != 0 {
		t.Errorf("nothing spent this coin, so it is not a double-spend rejection: %d",
			summary.CreatedThenSpentThenAbsent)
	}
	if !strings.Contains(summary.String(), "NEW_MISSING at run scope") {
		t.Errorf("the rendered summary must say what this means, got:\n%s", summary.String())
	}
}

// TestSummarizeExcusesACoinSpentInBetween is the other half, and the one that keeps the pass honest.
// A coin created, spent, and only then reported unresolvable is an ordinary double-spend rejection.
// Reporting it as a loss would manufacture exactly the NEW_MISSING finding that would send someone
// rewriting the acceptance-apply path over correct behaviour.
func TestSummarizeExcusesACoinSpentInBetween(t *testing.T) {
	records := []Record{
		{BlockHash: "creator", IBDStage: StageChainReplay, AcceptedTxIDs: []string{"coin-tx"}},
		{BlockHash: "spent-here", IBDStage: StageChainReplay, AcceptedSpends: []string{"coin-tx:0"}},
		{BlockHash: "respender", IBDStage: StageChainReplay, Error: "missing-input",
			MissingOutpoints: []MissingOutpoint{{TxID: "coin-tx", Index: 0}}},
	}

	summary := Summarize(records)
	if len(summary.CreatedThenLost) != 0 {
		t.Errorf("a coin spent before the block that tripped over it is not lost: %+v", summary.CreatedThenLost)
	}
	if summary.CreatedThenSpentThenAbsent != 1 {
		t.Errorf("expected one double-spend rejection, got %d", summary.CreatedThenSpentThenAbsent)
	}
}

// TestSummarizeIgnoresASpendAfterTheFact pins the ordering rule. A spend recorded after the block
// that could not resolve the coin explains nothing about why that block could not resolve it, and
// treating it as an excuse would silently drop a real loss.
func TestSummarizeIgnoresASpendAfterTheFact(t *testing.T) {
	records := []Record{
		{BlockHash: "creator", IBDStage: StageChainReplay, AcceptedTxIDs: []string{"coin-tx"}},
		{BlockHash: "respender", IBDStage: StageChainReplay, Error: "missing-input",
			MissingOutpoints: []MissingOutpoint{{TxID: "coin-tx", Index: 0}}},
		{BlockHash: "spent-later", IBDStage: StageChainReplay, AcceptedSpends: []string{"coin-tx:0"}},
	}

	summary := Summarize(records)
	if len(summary.CreatedThenLost) != 1 {
		t.Fatalf("a spend recorded after the failure does not excuse it: %+v", summary.CreatedThenLost)
	}
	if summary.CreatedThenSpentThenAbsent != 0 {
		t.Errorf("expected no double-spend rejection, got %d", summary.CreatedThenSpentThenAbsent)
	}
}

// TestSummarizeFlagsIncompleteSpendHistory: with the accepted-spends list capped, "no spend
// recorded" stops meaning "no spend happened", and a run-scope loss count that does not say so is
// overstating itself.
func TestSummarizeFlagsIncompleteSpendHistory(t *testing.T) {
	records := []Record{
		{BlockHash: "creator", IBDStage: StageChainReplay, AcceptedTxIDs: []string{"coin-tx"},
			AcceptedSpends: []string{"other:0"}, AcceptedSpendsTruncated: 12},
		{BlockHash: "respender", IBDStage: StageChainReplay, Error: "missing-input",
			MissingOutpoints: []MissingOutpoint{{TxID: "coin-tx", Index: 0}}},
	}

	summary := Summarize(records)
	if !summary.SpendHistoryIncomplete {
		t.Fatal("a truncated accepted-spends list must be reported, or the loss count reads as exact")
	}
	rendered := summary.String()
	if !strings.Contains(rendered, "upper bound") {
		t.Errorf("the summary must say the count is an upper bound when spends were dropped, got:\n%s", rendered)
	}
}

// TestSummarizeRefusesToCallCoinsLostWithNoSpendHistory is the guard against the run-scope pass's
// own worst failure mode. A survey written by a build that did not record accepted spends looks
// exactly like a run in which nothing was ever spent, and on that reading every ordinary
// double-spend rejection becomes a lost coin. The pass would then report thousands of NEW_MISSING
// findings - the loudest possible result - from no evidence at all.
func TestSummarizeRefusesToCallCoinsLostWithNoSpendHistory(t *testing.T) {
	records := []Record{
		{BlockHash: "creator", IBDStage: StageChainReplay, AcceptedTxIDs: []string{"coin-tx"}},
		{BlockHash: "respender", IBDStage: StageChainReplay, Error: "missing-input",
			MissingOutpoints: []MissingOutpoint{{TxID: "coin-tx", Index: 0}}},
	}

	summary := Summarize(records)
	if !summary.SpendHistoryAbsent {
		t.Fatal("a run with accepted transactions but no recorded spends has no spend history, and " +
			"saying otherwise turns absent evidence into a finding")
	}
	rendered := summary.String()
	if strings.Contains(rendered, "NEW_MISSING at run scope") {
		t.Errorf("with no spend history the pass must not claim NEW_MISSING, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "NOT evidence") {
		t.Errorf("the summary must say the count proves nothing without spend history, got:\n%s", rendered)
	}
}

// A run that recorded spends, even if none of them are relevant, does have spend history - the
// absent-history warning must not fire there and mask a real finding.
func TestSummarizeReportsLossWhenSpendHistoryExists(t *testing.T) {
	records := []Record{
		{BlockHash: "creator", IBDStage: StageChainReplay, AcceptedTxIDs: []string{"coin-tx"},
			AcceptedSpends: []string{"unrelated:0"}},
		{BlockHash: "respender", IBDStage: StageChainReplay, Error: "missing-input",
			MissingOutpoints: []MissingOutpoint{{TxID: "coin-tx", Index: 0}}},
	}

	summary := Summarize(records)
	if summary.SpendHistoryAbsent {
		t.Fatal("this run recorded a spend, so its spend history is present, merely irrelevant")
	}
	if len(summary.CreatedThenLost) != 1 {
		t.Fatalf("expected the lost coin to be reported, got %+v", summary.CreatedThenLost)
	}
	if !strings.Contains(summary.String(), "NEW_MISSING at run scope") {
		t.Error("with spend history present the finding should be stated plainly")
	}
}

// TestSummarizeScopesCreatedThenAbsentPerRun is the appended-file trap. A survey file is never
// replaced, so one file routinely holds several syncs, and after a --reset-db the database between
// them is not the same database. Comparing a coin created in one sync against its absence in the
// next turns ordinary "this sync has not re-created that coin yet" into a lost coin - and it would
// do so for thousands of coins at once, which is the loudest false finding this tool can produce.
func TestSummarizeScopesCreatedThenAbsentPerRun(t *testing.T) {
	records := []Record{
		{RunID: "run-a", BlockHash: "a-creator", IBDStage: StageChainReplay,
			AcceptedTxIDs: []string{"coin-tx"}, AcceptedSpends: []string{"unrelated:0"}},
		{RunID: "run-b", BlockHash: "b-spender", IBDStage: StageChainReplay, Error: "missing-input",
			AcceptedSpends:   []string{"unrelated:1"},
			MissingOutpoints: []MissingOutpoint{{TxID: "coin-tx", Index: 0}}},
	}

	summary := Summarize(records)
	if summary.Runs != 2 {
		t.Fatalf("expected the file to be read as 2 runs, got %d", summary.Runs)
	}
	if len(summary.CreatedThenLost) != 0 {
		t.Errorf("a coin created in one run and absent in another is not a loss: %+v", summary.CreatedThenLost)
	}
	if !strings.Contains(summary.String(), "scoped within each run") {
		t.Errorf("the summary must say the analysis was scoped per run, got:\n%s", summary.String())
	}
}

// Within one run the finding must still be reported - the scoping must not silence real losses.
func TestSummarizeStillFindsLossWithinASingleRun(t *testing.T) {
	records := []Record{
		{RunID: "run-a", BlockHash: "creator", IBDStage: StageChainReplay,
			AcceptedTxIDs: []string{"coin-tx"}, AcceptedSpends: []string{"unrelated:0"}},
		{RunID: "run-a", BlockHash: "spender", IBDStage: StageChainReplay, Error: "missing-input",
			MissingOutpoints: []MissingOutpoint{{TxID: "coin-tx", Index: 0}}},
	}

	summary := Summarize(records)
	if len(summary.CreatedThenLost) != 1 {
		t.Fatalf("a loss within one run must still be reported: %+v", summary.CreatedThenLost)
	}
}

// TestSummarizeExcusesACoinSpentByTheSameBlock is the commonest double spend there is: one mergeset
// holds two transactions spending the same coin, the first is accepted and the second fails with a
// missing input. The spend and the failure are then in the SAME record, and an ordering test that
// demands a strictly earlier spend calls that a lost coin.
//
// Found against a real 81,701-block survey, where it produced the investigation's only NEW_MISSING
// candidate - one coin, which the record itself showed as present in the parent set and spent by the
// very block reporting it missing.
func TestSummarizeExcusesACoinSpentByTheSameBlock(t *testing.T) {
	records := []Record{
		{RunID: "r", BlockHash: "creator", IBDStage: StageChainReplay,
			AcceptedTxIDs: []string{"coin-tx"}, AcceptedSpends: []string{"unrelated:0"}},
		{RunID: "r", BlockHash: "double-spender", IBDStage: StageChainReplay, Error: "missing-input",
			AcceptedSpends:   []string{"coin-tx:0"},
			MissingOutpoints: []MissingOutpoint{{TxID: "coin-tx", Index: 0}}},
	}

	summary := Summarize(records)
	if len(summary.CreatedThenLost) != 0 {
		t.Errorf("a coin spent by the same block that reports it missing is a double spend, not a "+
			"loss: %+v", summary.CreatedThenLost)
	}
	if summary.CreatedThenSpentThenAbsent != 1 {
		t.Errorf("expected it counted as a double-spend rejection, got %d", summary.CreatedThenSpentThenAbsent)
	}
}

// TestSummarizeExcludesAmbiguousCreation covers the DAG realities the pass cannot model. A
// transaction accepted by two different blocks - a reorg re-accepting it on the new chain, or two
// blocks whose byte-identical coinbases share a transaction ID - has no single creation point, and
// neither does one that was accepted somewhere and rejected somewhere else. "Created here, absent
// later" is then a statement about nothing, and reporting it as a lost coin is a guess dressed as a
// finding.
//
// Both shapes turned up in a real 125,159-block survey, as the run's only NEW_MISSING candidate.
func TestSummarizeExcludesAmbiguousCreation(t *testing.T) {
	tests := []struct {
		name    string
		records []Record
	}{{
		name: "accepted by two different blocks",
		records: []Record{
			{RunID: "r", BlockHash: "creator-a", IBDStage: StageChainReplay,
				AcceptedTxIDs: []string{"coin-tx"}, AcceptedSpends: []string{"unrelated:0"}},
			{RunID: "r", BlockHash: "creator-b", IBDStage: StageChainReplay,
				AcceptedTxIDs: []string{"coin-tx"}},
			{RunID: "r", BlockHash: "spender", IBDStage: StageChainReplay, Error: "missing-input",
				MissingOutpoints: []MissingOutpoint{{TxID: "coin-tx", Index: 0}}},
		},
	}, {
		name: "accepted in one block and rejected in another",
		records: []Record{
			{RunID: "r", BlockHash: "creator", IBDStage: StageChainReplay,
				AcceptedTxIDs: []string{"coin-tx"}, AcceptedSpends: []string{"unrelated:0"}},
			{RunID: "r", BlockHash: "rejector", IBDStage: StageChainReplay,
				RejectedOrRedTxIDs: []string{"coin-tx"}},
			{RunID: "r", BlockHash: "spender", IBDStage: StageChainReplay, Error: "missing-input",
				MissingOutpoints: []MissingOutpoint{{TxID: "coin-tx", Index: 0}}},
		},
	}}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			summary := Summarize(test.records)
			if len(summary.CreatedThenLost) != 0 {
				t.Errorf("a coin with no settled creation point must not be called lost: %+v",
					summary.CreatedThenLost)
			}
			if summary.AmbiguousCreation != 1 {
				t.Errorf("expected it counted as ambiguous, got %d", summary.AmbiguousCreation)
			}
			if !strings.Contains(summary.String(), "no single creation point") {
				t.Errorf("the summary must explain the exclusion, got:\n%s", summary.String())
			}
		})
	}
}

// TestSummarizeSplitsSelfInflictedFromInheritedGap is the measurement that decides what a fix has to
// do, and the two cases are indistinguishable without it. Both coins below are simply absent - no
// entry anywhere, nothing in the failing block's mergeset creating them - so both land under
// ORIGINAL_MISSING and both read as "the imported snapshot never had it".
//
// One of them was in fact never created BY THIS NODE, because the node had already lost the input of
// the transaction that would have created it and silently marked that transaction unaccepted. A
// clean UTXO set handed to a node doing that would start degrading again immediately, so counting it
// as inherited damage would hide the reason a repair alone cannot hold.
func TestSummarizeSplitsSelfInflictedFromInheritedGap(t *testing.T) {
	records := []Record{{
		RunID:                        "r",
		BlockHash:                    "rejector",
		IBDStage:                     StageChainReplay,
		RejectedOrRedTxIDs:           []string{"starved-tx"},
		RejectionReasons:             map[string]int{"missing-input": 1},
		RejectedForMissingInputTxIDs: []string{"starved-tx"},
	}, {
		RunID:          "r",
		BlockHash:      "spender",
		IBDStage:       StageChainReplay,
		Error:          "missing-input",
		Classification: ClassificationOriginalMissing,
		MissingOutpoints: []MissingOutpoint{
			// Created by the transaction this node starved of its input: self-inflicted.
			{TxID: "starved-tx", Index: 0},
			// Never seen at all in this run: inherited with the imported set.
			{TxID: "never-seen-tx", Index: 0},
		},
	}}

	summary := Summarize(records)
	if summary.SelfInflictedMissing != 1 {
		t.Errorf("expected 1 self-inflicted missing coin, got %d", summary.SelfInflictedMissing)
	}
	if summary.LostAfterCreation != 0 || summary.DoubleSpendMissing != 0 {
		t.Errorf("neither coin was created by an accepted transaction: lost=%d doubleSpend=%d",
			summary.LostAfterCreation, summary.DoubleSpendMissing)
	}
	if summary.InheritedMissing != 1 {
		t.Errorf("expected 1 inherited missing coin, got %d", summary.InheritedMissing)
	}
	if summary.RejectionReasons["missing-input"] != 1 {
		t.Errorf("rejection reasons should be totalled over the run, got %v", summary.RejectionReasons)
	}

	rendered := summary.String()
	if !strings.Contains(rendered, "self-inflicted") || !strings.Contains(rendered, "inherited") {
		t.Errorf("the summary must name both origins, got:\n%s", rendered)
	}
	// The consequence is the point: repairing the set is not enough while this is non-zero.
	if !strings.Contains(rendered, "not\n  sufficient") && !strings.Contains(rendered, "not sufficient") {
		t.Errorf("the summary must say a repair alone will not hold, got:\n%s", rendered)
	}
}

// With nothing self-inflicted, the warning must not fire - it would argue against a repair that is
// in fact sufficient.
func TestSummarizeDoesNotWarnWhenNothingIsSelfInflicted(t *testing.T) {
	summary := Summarize([]Record{{
		RunID: "r", BlockHash: "spender", IBDStage: StageChainReplay, Error: "missing-input",
		MissingOutpoints: []MissingOutpoint{{TxID: "never-seen-tx", Index: 0}},
	}})

	if summary.SelfInflictedMissing != 0 {
		t.Errorf("nothing was rejected for a missing input, got %d", summary.SelfInflictedMissing)
	}
	if strings.Contains(summary.String(), "not sufficient") {
		t.Error("the repair-is-not-enough warning must not fire when nothing is self-inflicted")
	}
}

// TestSummarizeTracesCascadeToItsSeed is the root-cause walk. A gap does not stay one coin: the
// absent coin starves a transaction, the coins that transaction would have created are absent in
// turn, and whatever spends those is starved as well. Counting the absences says how bad it is;
// following them upstream says which single coin to fix.
//
// Only the seed is worth repairing - restore it and everything downstream reappears on its own,
// while restoring a downstream coin fixes exactly that coin and nothing else.
func TestSummarizeTracesCascadeToItsSeed(t *testing.T) {
	records := []Record{{
		RunID:     "r",
		BlockHash: "block",
		IBDStage:  StageChainReplay,
		StarvedTransactions: []StarvedTransaction{
			// tx-a could not find seed-tx:0. Nothing starved seed-tx, so that coin is the seed.
			{TxID: "tx-a", MissingOutpoints: []string{"seed-tx:0"}},
			// tx-b could not find a coin tx-a would have created - downstream of the same seed.
			{TxID: "tx-b", MissingOutpoints: []string{"tx-a:0"}},
			// tx-c could not find a coin tx-b would have created - two hops down.
			{TxID: "tx-c", MissingOutpoints: []string{"tx-b:1"}},
		},
	}}

	summary := Summarize(records)
	if len(summary.CascadeSeeds) != 1 {
		t.Fatalf("only the coin nothing starved is a seed, got %+v", summary.CascadeSeeds)
	}
	seed := summary.CascadeSeeds[0]
	if seed.Outpoint != "seed-tx:0" {
		t.Errorf("expected seed-tx:0 to be the seed, got %s", seed.Outpoint)
	}
	if seed.StarvedDirectly != 1 {
		t.Errorf("expected 1 transaction starved directly, got %d", seed.StarvedDirectly)
	}
	// tx-b and tx-c are both downstream of the one seed.
	if seed.StarvedDownstream != 2 {
		t.Errorf("expected 2 transactions starved downstream of the seed, got %d", seed.StarvedDownstream)
	}
	if summary.CascadeDepth < 2 {
		t.Errorf("expected the chain to be walked at least 2 deep, got %d", summary.CascadeDepth)
	}
	if !strings.Contains(summary.String(), "removes more than itself") {
		t.Errorf("the summary must say why only seeds are worth repairing, got:\n%s", summary.String())
	}
}

// A coin whose creating transaction was itself starved is a link, not a cause, and must not be
// offered as something to repair - fixing it leaves the thing that caused it untouched.
func TestSummarizeDoesNotCallADownstreamCoinASeed(t *testing.T) {
	summary := Summarize([]Record{{
		RunID: "r", BlockHash: "block", IBDStage: StageChainReplay,
		StarvedTransactions: []StarvedTransaction{
			{TxID: "upstream-tx", MissingOutpoints: []string{"real-seed:0"}},
			{TxID: "downstream-tx", MissingOutpoints: []string{"upstream-tx:3"}},
		},
	}})

	for _, seed := range summary.CascadeSeeds {
		if seed.Outpoint == "upstream-tx:3" {
			t.Errorf("upstream-tx was starved, so the coin it would have created is a link, not a seed: %+v",
				summary.CascadeSeeds)
		}
	}
	if len(summary.CascadeSeeds) != 1 || summary.CascadeSeeds[0].Outpoint != "real-seed:0" {
		t.Errorf("expected exactly real-seed:0, got %+v", summary.CascadeSeeds)
	}
}

// TestSummarizeDoesNotCallADuplicateRejectionSelfInflicted is the confounder that made the first
// version of this measurement report 82% self-inflicted damage on a mainnet survey when the true
// figure was zero.
//
// On a DAG the same transaction is routinely included in several blocks: one block accepts it, the
// others reject it because its input has already been consumed. That rejection is recorded as
// "missing-input" and looks exactly like a transaction starved by a real gap - but the coins it
// creates exist, because another block accepted it. Only a transaction this node never managed to
// accept anywhere actually failed to create anything.
func TestSummarizeDoesNotCallADuplicateRejectionSelfInflicted(t *testing.T) {
	records := []Record{{
		RunID: "r", BlockHash: "accepting-block", IBDStage: StageChainReplay,
		AcceptedTxIDs: []string{"dup-tx"}, AcceptedSpends: []string{"unrelated:0"},
	}, {
		RunID: "r", BlockHash: "rejecting-block", IBDStage: StageChainReplay,
		RejectedOrRedTxIDs:           []string{"dup-tx"},
		RejectionReasons:             map[string]int{"missing-input": 1},
		RejectedForMissingInputTxIDs: []string{"dup-tx"},
	}, {
		RunID: "r", BlockHash: "spender", IBDStage: StageChainReplay, Error: "missing-input",
		MissingOutpoints: []MissingOutpoint{{TxID: "dup-tx", Index: 0}},
	}}

	summary := Summarize(records)
	if summary.SelfInflictedMissing != 0 {
		t.Errorf("dup-tx was accepted elsewhere, so it created its coins - not self-inflicted: %d",
			summary.SelfInflictedMissing)
	}
	// It was created and never spent, so by this run's evidence it is a genuine loss - which is a
	// different finding, and the one that should be reported.
	if summary.LostAfterCreation != 1 {
		t.Errorf("expected the coin to be reported as lost after creation, got %d", summary.LostAfterCreation)
	}
}

// A coin created, spent, and then wanted again is ordinary and must land in neither damage bucket.
func TestSummarizeCountsAnOrdinaryDoubleSpendSeparately(t *testing.T) {
	summary := Summarize([]Record{{
		RunID: "r", BlockHash: "creator", IBDStage: StageChainReplay, AcceptedTxIDs: []string{"tx"},
	}, {
		RunID: "r", BlockHash: "spender", IBDStage: StageChainReplay, AcceptedSpends: []string{"tx:0"},
	}, {
		RunID: "r", BlockHash: "respender", IBDStage: StageChainReplay, Error: "missing-input",
		MissingOutpoints: []MissingOutpoint{{TxID: "tx", Index: 0}},
	}})

	if summary.DoubleSpendMissing != 1 {
		t.Errorf("expected 1 ordinary double spend, got %d", summary.DoubleSpendMissing)
	}
	if summary.LostAfterCreation != 0 || summary.SelfInflictedMissing != 0 || summary.InheritedMissing != 0 {
		t.Errorf("an ordinary double spend is not damage: lost=%d self=%d inherited=%d",
			summary.LostAfterCreation, summary.SelfInflictedMissing, summary.InheritedMissing)
	}
}
