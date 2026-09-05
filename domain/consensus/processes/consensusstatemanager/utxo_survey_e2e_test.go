package consensusstatemanager_test

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus"
	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/testapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/multiset"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/testutils"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/utxosurvey"
	"github.com/HoosatNetwork/HTND/util/staging"
)

// chainLength is how many blocks after genesis each fixture builds. The first is the one whose
// multiset gets corrupted; the rest are the ones that must all end up in the survey.
const chainLength = 5

// TestUTXOSurveyRecordsEveryToleratedFailure covers the regime a node on an incomplete
// pruning-point UTXO set actually runs in: the offset propagates to every block, verifyUTXO
// tolerates each one so virtual resolution can advance, and logToleratedIssue warns once for the
// whole process and debug-logs everything after it. Every one of those blocks failed its commitment
// check, and the survey has to hold a record for every one of them.
//
// Before the survey existed there was no per-block trace of any of this at all - a run of thousands
// of tolerated failures left one warn line behind.
func TestUTXOSurveyRecordsEveryToleratedFailure(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		tc, chain, surveyPath, teardown := offsetChainFixture(t, consensusConfig,
			"TestUTXOSurveyRecordsEveryToleratedFailure", false)
		defer teardown(false)

		status, err := tc.ConsensusStateManager().ResolveBlockStatusTest(
			model.NewStagingArea(), chain[len(chain)-1], false)
		if err != nil {
			t.Fatalf("ResolveBlockStatusTest: %+v", err)
		}
		// The offset is tolerated, so the blocks stay valid - which is exactly why nothing but this
		// survey records that they failed.
		if status != externalapi.StatusUTXOValid {
			t.Fatalf("expected the inherited offset to be tolerated and the chain to resolve valid, got %s", status)
		}

		assertEveryBlockSurveyed(t, surveyPath, chain[1:])
	})
}

// TestUTXOSurveyRecordsBlocksDisqualifiedByInheritance covers the other way a run of failures
// collapses into one: once a chain block is disqualified, ResolveBlockStatus resolves every
// descendant through its cascade branch, which never calls verifyUTXO. Those blocks are disqualified
// without anyone ever asking what they would have failed on, so the root's error is the only one
// anybody sees. surveyCascadedBlock asks anyway and throws the answer away, and the file below is
// how we know it did: without it this test finds zero records, not four.
func TestUTXOSurveyRecordsBlocksDisqualifiedByInheritance(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		tc, chain, surveyPath, teardown := offsetChainFixture(t, consensusConfig,
			"TestUTXOSurveyRecordsBlocksDisqualifiedByInheritance", true)
		defer teardown(false)

		status, err := tc.ConsensusStateManager().ResolveBlockStatusTest(
			model.NewStagingArea(), chain[len(chain)-1], false)
		if err != nil {
			t.Fatalf("ResolveBlockStatusTest: %+v", err)
		}
		// Proves the cascade branch is the one that ran: the tip inherited its status rather than
		// earning it.
		if status != externalapi.StatusDisqualifiedFromChain {
			t.Fatalf("expected the tip to be disqualified by inheritance from the root, got %s", status)
		}

		assertEveryBlockSurveyed(t, surveyPath, chain[1:])
	})
}

// offsetChainFixture builds a healthy chain, then puts the node into the state a real one reaches
// after a bad pruning-point import: the first chain block's stored multiset no longer hashes to
// what its own header committed to, and everything after it is unverified. Because MuHash is
// homomorphic, every later block inherits that exact offset, so all of them fail their commitment
// check for a reason none of them caused - which is the condition this whole survey exists to
// measure.
//
// disqualifyRoot additionally marks the corrupted block disqualified, which is what sends its
// descendants down ResolveBlockStatus's cascade branch instead of resolveSingleBlockStatus.
func offsetChainFixture(t *testing.T, consensusConfig *consensus.Config, name string, disqualifyRoot bool) (
	testapi.TestConsensus, []*externalapi.DomainHash, string, func(keepDataDir bool),
) {
	t.Helper()
	consensusConfig.BlockCoinbaseMaturity = 0

	surveyPath := filepath.Join(t.TempDir(), "survey.jsonl")
	t.Setenv("HTND_UTXO_SURVEY", surveyPath)
	t.Setenv("HTND_UTXO_SURVEY_MAX", "0")
	utxosurvey.Reset()
	t.Cleanup(utxosurvey.Reset)

	factory := consensus.NewFactory()
	tc, teardown, err := factory.NewTestConsensus(consensusConfig, name)
	if err != nil {
		t.Fatalf("Error setting up consensus: %+v", err)
	}

	chain := make([]*externalapi.DomainHash, 0, chainLength)
	tipHash := consensusConfig.GenesisHash
	for range chainLength {
		tipHash, _, err = tc.AddBlock([]*externalapi.DomainHash{tipHash}, nil, nil)
		if err != nil {
			teardown(false)
			t.Fatalf("AddBlock: %+v", err)
		}
		chain = append(chain, tipHash)
	}

	stagingArea := model.NewStagingArea()
	offsetMultiset := multiset.New()
	offsetMultiset.Add([]byte("a UTXO set this chain never committed to"))
	tc.MultisetStore().Stage(stagingArea, chain[0], offsetMultiset)
	if disqualifyRoot {
		tc.BlockStatusStore().Stage(stagingArea, chain[0], externalapi.StatusDisqualifiedFromChain)
	}
	for _, blockHash := range chain[1:] {
		tc.BlockStatusStore().Stage(stagingArea, blockHash, externalapi.StatusUTXOPendingVerification)
	}
	if err := staging.CommitAllChanges(tc.DatabaseContext(), stagingArea); err != nil {
		teardown(false)
		t.Fatalf("committing the offset fixture: %+v", err)
	}

	return tc, chain, surveyPath, teardown
}

// assertEveryBlockSurveyed is the actual Phase 1 assertion: not "a failure was recorded" but "every
// failure was recorded", with each record carrying enough to be clustered and classified without
// going back to the node.
func assertEveryBlockSurveyed(t *testing.T, surveyPath string, expectedBlocks []*externalapi.DomainHash) {
	t.Helper()

	records := readSurveyRecords(t, surveyPath)
	if len(records) == 0 {
		t.Fatal("the survey recorded nothing at all for a run of blocks that every one of which failed " +
			"its UTXO commitment check")
	}

	recordsByBlock := make(map[string]utxosurvey.Record, len(records))
	for _, record := range records {
		recordsByBlock[record.BlockHash] = record
	}
	if len(recordsByBlock) != len(expectedBlocks) {
		t.Errorf("expected one record per failing block (%d), got %d distinct blocks in the survey - "+
			"the survey is still keeping a subset of the failures", len(expectedBlocks), len(recordsByBlock))
	}

	for i, blockHash := range expectedBlocks {
		record, ok := recordsByBlock[blockHash.String()]
		if !ok {
			t.Errorf("failing block %d (%s) is missing from the survey", i, blockHash)
			continue
		}
		if !strings.Contains(record.Error, "ErrBadUTXOCommitment") {
			t.Errorf("block %d (%s): expected an ErrBadUTXOCommitment record, got %q", i, blockHash, record.Error)
		}
		if record.HeaderUTXOCommitment == "" || record.CalculatedUTXOCommitment == "" {
			t.Errorf("block %d (%s): a commitment mismatch record must carry both commitments, got "+
				"header=%q calculated=%q", i, blockHash, record.HeaderUTXOCommitment, record.CalculatedUTXOCommitment)
		}
		if record.HeaderUTXOCommitment == record.CalculatedUTXOCommitment {
			t.Errorf("block %d (%s): the record's two commitments are equal, so it does not describe the "+
				"failure it was written for", i, blockHash)
		}
		if record.SelectedParent == "" {
			t.Errorf("block %d (%s): record has no selected parent, so it cannot be clustered against the "+
				"chain it sits on", i, blockHash)
		}
		if record.IBDStage != utxosurvey.StageChainReplay {
			t.Errorf("block %d (%s): expected stage %q, got %q", i, blockHash,
				utxosurvey.StageChainReplay, record.IBDStage)
		}
		// Nothing here actually lost a coin: every block's own delta agrees with its own acceptance
		// data, and only the inherited multiset is off. Getting that right matters as much as catching
		// a real loss - a survey that called this ORIGINAL_MISSING would send the investigation after a
		// bug that is not there.
		if record.Classification != utxosurvey.ClassificationCommitmentOnly {
			t.Errorf("block %d (%s): expected %s, got %s (notes: %s)", i, blockHash,
				utxosurvey.ClassificationCommitmentOnly, record.Classification, record.Notes)
		}
		if len(record.MissingOutpoints) != 0 {
			t.Errorf("block %d (%s): no input was unresolvable, yet the record names %d missing outpoints",
				i, blockHash, len(record.MissingOutpoints))
		}
		if !strings.Contains(record.Notes, "inherited offset") {
			t.Errorf("block %d (%s): the record should name the selected parent as the offset's source, "+
				"got notes %q", i, blockHash, record.Notes)
		}
	}

	// The whole point of the exercise: the run has to be readable as a cluster, so the record for the
	// first failing block must show the offset entering the chain from its parent, and every later
	// record must show it being carried forward.
	first, ok := recordsByBlock[expectedBlocks[0].String()]
	if ok && first.ParentStoredMultiset != first.CalculatedUTXOCommitment {
		t.Errorf("the first failing block's calculated commitment (%s) should be exactly the offset "+
			"multiset it inherited from its parent (%s) - it merges nothing that changes it",
			first.CalculatedUTXOCommitment, first.ParentStoredMultiset)
	}
}

func readSurveyRecords(t *testing.T, path string) []utxosurvey.Record {
	t.Helper()
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("opening survey file: %+v", err)
	}
	defer file.Close()

	var records []utxosurvey.Record
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var record utxosurvey.Record
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
