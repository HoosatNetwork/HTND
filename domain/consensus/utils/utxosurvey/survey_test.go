package utxosurvey

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// enableSurvey points the survey at a fresh file in the test's temp directory and restores the
// previous configuration afterwards. Reset is what makes this possible: the configuration is read
// lazily, so a test can change it mid-process.
func enableSurvey(t *testing.T, max int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "survey.jsonl")
	t.Setenv("HTND_UTXO_SURVEY", path)
	if max >= 0 {
		t.Setenv("HTND_UTXO_SURVEY_MAX", itoa(max))
	}
	Reset()
	t.Cleanup(Reset)
	return path
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}

func readRecords(t *testing.T, path string) []Record {
	t.Helper()
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("opening survey file: %+v", err)
	}
	defer file.Close()

	var records []Record
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var record Record
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("survey line is not valid JSON (%s): %+v", line, err)
		}
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("reading survey file: %+v", err)
	}
	return records
}

// TestSurveyDisabledByDefault pins the property that makes this safe to leave in the tree: with no
// environment set, nothing is enabled and nothing is written.
func TestSurveyDisabledByDefault(t *testing.T) {
	t.Setenv("HTND_UTXO_SURVEY", "")
	Reset()
	t.Cleanup(Reset)

	if Enabled() {
		t.Fatal("survey reports itself enabled with HTND_UTXO_SURVEY unset")
	}
	// Writing anyway must not create a file, panic, or otherwise misbehave.
	Write(&Record{BlockHash: "should-not-be-written"})
	if path := Path(); path != "" {
		t.Fatalf("expected no survey path, got %q", path)
	}
}

// TestSurveyRecordsEveryFailure is the whole point of the package: a run with many failures keeps
// all of them, in order, one JSON object per line.
func TestSurveyRecordsEveryFailure(t *testing.T) {
	path := enableSurvey(t, 0)

	blockHashes := []string{"block-a", "block-b", "block-c", "block-d"}
	for i, blockHash := range blockHashes {
		Write(&Record{
			BlockHash:      blockHash,
			DAAScore:       uint64(i),
			Error:          "ErrBadUTXOCommitment",
			Classification: ClassificationCommitmentOnly,
		})
	}

	records := readRecords(t, path)
	if len(records) != len(blockHashes) {
		t.Fatalf("expected %d records, got %d - the survey kept only some of the failures",
			len(blockHashes), len(records))
	}
	for i, record := range records {
		if record.BlockHash != blockHashes[i] {
			t.Errorf("record %d: expected block %s, got %s", i, blockHashes[i], record.BlockHash)
		}
	}
}

// TestSurveyRespectsCap checks the cap stops the file growing without bound, and that it stops it
// at the cap rather than at the first record.
func TestSurveyRespectsCap(t *testing.T) {
	path := enableSurvey(t, 3)

	for i := range 10 {
		Write(&Record{BlockHash: "block", DAAScore: uint64(i)})
	}

	records := readRecords(t, path)
	if len(records) != 3 {
		t.Fatalf("expected the 3-record cap to be honoured, got %d records", len(records))
	}
	if Enabled() {
		t.Fatal("survey should report itself disabled once the cap is reached")
	}
	for i, record := range records {
		if record.DAAScore != uint64(i) {
			t.Errorf("record %d: expected the first %d records to be kept, got daaScore %d",
				i, 3, record.DAAScore)
		}
	}
}

// TestSurveyRoundTripsAllFields guards the schema: everything a classification pass reads has to
// survive the trip through JSON, including the pointer-valued expectedBlockDAAScore whose null is
// meaningful.
func TestSurveyRoundTripsAllFields(t *testing.T) {
	path := enableSurvey(t, 0)

	expectedDAAScore := uint64(4242)
	written := &Record{
		BlockHash:                "hash",
		SelectedParent:           "parent",
		DAAScore:                 100,
		BlueScore:                99,
		IsChainBlock:             true,
		IBDStage:                 StageChainReplay,
		Error:                    "ErrBadUTXOCommitment+missing-input",
		HeaderUTXOCommitment:     "header",
		CalculatedUTXOCommitment: "calculated",
		ParentStoredMultiset:     "parent-stored",
		ParentRecomputedMultiset: "parent-recomputed",
		AcceptanceTxCount:        2,
		AcceptedTxIDs:            []string{"tx1", "tx2"},
		RejectedOrRedTxIDs:       []string{"tx3"},
		CoinbaseTxID:             "coinbase",
		MissingOutpoints: []MissingOutpoint{{
			TxID:                              "missing-tx",
			Index:                             7,
			SpentByTx:                         "spender",
			ExpectedBlockDAAScore:             &expectedDAAScore,
			FoundInParentSet:                  true,
			FoundUnderDifferentDAAScore:       true,
			FoundUnderDifferentAmountOrScript: false,
			AlternateMatches: []AlternateMatch{{
				Source:         SourceVirtualUTXOSet,
				Amount:         500,
				BlockDAAScore:  17,
				SerializedUTXO: "deadbeef",
			}},
		}},
		ExtraAddsNotInHeaderView: []DiffElement{{TxID: "extra", Reason: "add-not-in-acceptance-data"}},
		Classification:           ClassificationHandlingMismatch,
		Notes:                    "note",
	}
	Write(written)

	records := readRecords(t, path)
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	readBack := records[0]

	if readBack.Classification != ClassificationHandlingMismatch {
		t.Errorf("classification did not round-trip: %s", readBack.Classification)
	}
	if len(readBack.MissingOutpoints) != 1 {
		t.Fatalf("missing outpoints did not round-trip: %+v", readBack.MissingOutpoints)
	}
	missing := readBack.MissingOutpoints[0]
	if missing.ExpectedBlockDAAScore == nil || *missing.ExpectedBlockDAAScore != expectedDAAScore {
		t.Errorf("expectedBlockDAAScore did not round-trip: %v", missing.ExpectedBlockDAAScore)
	}
	if len(missing.AlternateMatches) != 1 || missing.AlternateMatches[0].SerializedUTXO != "deadbeef" {
		t.Errorf("alternate matches did not round-trip: %+v", missing.AlternateMatches)
	}
	if len(readBack.ExtraAddsNotInHeaderView) != 1 {
		t.Errorf("extra adds did not round-trip: %+v", readBack.ExtraAddsNotInHeaderView)
	}

	// A record with nothing to say about a missing outpoint must serialize its expected DAA score as
	// null, not as 0 - "the block does not claim to create this coin" and "it creates it at score 0"
	// are different findings.
	Write(&Record{BlockHash: "second", MissingOutpoints: []MissingOutpoint{{TxID: "t"}}})
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading survey file: %+v", err)
	}
	if !strings.Contains(string(raw), `"expectedBlockDAAScore":null`) {
		t.Error("an outpoint no accepted transaction creates should serialize expectedBlockDAAScore as null")
	}
}
