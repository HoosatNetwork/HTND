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

// TestSummarizeIgnoresAlreadySpentCoins guards the most dangerous false positive: a coin absent
// because the block's own past already spent it is correct behaviour, and counting it as a loss
// would send the investigation after a bug that is not there.
func TestSummarizeIgnoresAlreadySpentCoins(t *testing.T) {
	summary := Summarize([]Record{{
		BlockHash:        "block",
		IBDStage:         StageChainReplay,
		Error:            "missing-input",
		MissingOutpoints: []MissingOutpoint{{TxID: "spent", Index: 0, AlreadySpentInThisPast: true}},
	}})

	if len(summary.AbsentEverywhere) != 0 {
		t.Errorf("a coin this block's own past already spent is not a missing coin: %+v", summary.AbsentEverywhere)
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
