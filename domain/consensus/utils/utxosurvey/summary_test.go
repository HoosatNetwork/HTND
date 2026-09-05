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
