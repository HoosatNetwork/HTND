package muhashjournal

import (
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/multiset"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/utxo"
)

func testPreimage(t *testing.T, b byte, amount, daaScore uint64, coinbase bool) []byte {
	t.Helper()
	outpoint := externalapi.NewDomainOutpoint(
		externalapi.NewDomainTransactionIDFromByteArray(&[externalapi.DomainHashSize]byte{b}), uint32(b))
	data, err := utxo.SerializeUTXO(utxo.NewUTXOEntry(amount, &externalapi.ScriptPublicKey{Script: []byte{0xac}},
		coinbase, daaScore), outpoint)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// testRecord builds a record the way calculateMultiset does: a parent state, then ops applied
// through a Recorder.
func testRecord(t *testing.T, block string, apply func(r *Recorder)) (*Record, model.Multiset) {
	t.Helper()
	parent := multiset.New()
	parent.Add(testPreimage(t, 100, 1000, 1, true))
	record := &Record{
		Kind:                     KindBlock,
		Block:                    block,
		DAAScore:                 50,
		ParentMultiset:           parent.Hash().String(),
		ParentMultisetSerialized: hex.EncodeToString(parent.Serialize()),
		MergeSet:                 []MergeSetBlock{{Hash: "sp", DAAScore: 48}, {Hash: "m", DAAScore: 49}},
	}
	ms := parent.Clone()
	recorder := NewRecorder(ms)
	apply(recorder)
	record.Ops = recorder.Ops()
	record.ResultMultiset = ms.Hash().String()
	return record, ms
}

func TestRecorderForwardsAndRecordsEveryOperation(t *testing.T) {
	spent := testPreimage(t, 100, 1000, 1, true)
	created := testPreimage(t, 1, 900, 50, false)
	record, ms := testRecord(t, "b", func(r *Recorder) {
		r.Remove(spent)
		r.Add(created)
	})

	expected := multiset.New()
	expected.Add(created)
	if !ms.Hash().Equal(expected.Hash()) {
		t.Fatalf("the recorder must forward to the multiset it wraps")
	}
	if len(record.Ops) != 2 || record.Ops[0].Op != OpRemove || record.Ops[1].Op != OpAdd {
		t.Fatalf("unexpected ops %+v", record.Ops)
	}
	if op := record.Ops[1]; op.Amount != 900 || op.DAAScore != 50 || op.Coinbase || op.Script != "ac" ||
		!strings.HasSuffix(op.Outpoint, ":1") {
		t.Errorf("op not decoded: %+v", op)
	}
	if err := record.Verify(); err != nil {
		t.Errorf("a faithful record must replay: %s", err)
	}
	record.ResultMultiset = expected.Hash().String() + "x"
	if err := record.Verify(); err == nil {
		t.Errorf("a record that does not replay must be reported")
	}
}

// TestWhatIfFindsTheSingleChange pins that the search recovers the change a miner made, for each
// kind it tries: the header is built from the "miner's" ops and the record from the node's.
func TestWhatIfFindsTheSingleChange(t *testing.T) {
	created := testPreimage(t, 1, 900, 50, false)
	other := testPreimage(t, 2, 800, 50, false)
	tests := []struct {
		name  string
		miner func(r *Recorder)
		want  string
	}{
		{"restamped", func(r *Recorder) { r.Add(testPreimage(t, 1, 900, 49, false)); r.Add(other) },
			"op 0 (add"},
		{"extra element", func(r *Recorder) { r.Add(other) }, "op 0 (add"},
		{"coinbase flag", func(r *Recorder) { r.Add(testPreimage(t, 1, 900, 50, true)); r.Add(other) }, "coinbase=true"},
		{"block-wide stamp", func(r *Recorder) {
			r.Add(testPreimage(t, 1, 900, 48, false))
			r.Add(testPreimage(t, 2, 800, 48, false))
		}, "every created entry carried DAA score 48"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, minerResult := testRecord(t, "b", test.miner)
			record, _ := testRecord(t, "b", func(r *Recorder) { r.Add(created); r.Add(other) })
			hits, _, err := WhatIf(record, minerResult.Hash().String(), 0)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, hit := range hits {
				if strings.Contains(hit.Description, test.want) {
					found = true
				}
			}
			if !found {
				t.Errorf("no hypothesis containing %q among %+v", test.want, hits)
			}
		})
	}
}

func TestMatchAndCompare(t *testing.T) {
	created := testPreimage(t, 1, 900, 50, false)
	restamped := testPreimage(t, 1, 900, 49, false)
	node, _ := testRecord(t, "mined", func(r *Recorder) { r.Add(created) })
	miner, minerResult := testRecord(t, "prospective", func(r *Recorder) { r.Add(restamped) })
	miner.Kind = KindTemplate
	node.HeaderCommitment = minerResult.Hash().String()

	pairs := Match([]*Record{node}, []*Record{miner})
	if len(pairs) != 1 || pairs[0].How != "result" {
		t.Fatalf("the miner's template must pair by result with the block it mined, got %+v", pairs)
	}
	difference := Compare(pairs[0])
	if !difference.ParentsAgree || len(difference.OnlyInA) != 1 || len(difference.OnlyInB) != 1 ||
		difference.OnlyInA[0].DAAScore != 50 || difference.OnlyInB[0].DAAScore != 49 {
		t.Errorf("unexpected difference %+v", difference)
	}

	same, _ := testRecord(t, "mined", func(r *Recorder) { r.Add(created) })
	if d := Compare(Match([]*Record{node}, []*Record{same})[0]); !d.Empty() {
		t.Errorf("identical ops on identical parents must compare empty, got %+v", d)
	}
}

func TestEntryBlocks(t *testing.T) {
	entry := &Record{HeaderCommitment: "h", ResultMultiset: "r", ParentHeaderCommitment: "p", ParentMultiset: "p"}
	inherited := &Record{HeaderCommitment: "h", ResultMultiset: "r", ParentHeaderCommitment: "p", ParentMultiset: "q"}
	clean := &Record{HeaderCommitment: "h", ResultMultiset: "h", ParentHeaderCommitment: "p", ParentMultiset: "p"}
	template := &Record{ResultMultiset: "r", ParentHeaderCommitment: "p", ParentMultiset: "p"}
	got := EntryBlocks([]*Record{entry, inherited, clean, template})
	if len(got) != 1 || got[0] != entry {
		t.Errorf("only the block whose parent matched and which does not must be an entry, got %+v", got)
	}
}

func TestSetPathTurnsTheJournalOnAndOff(t *testing.T) {
	defer SetPath("")
	SetPath("")
	if Enabled() {
		t.Fatalf("no path means off")
	}
	Write(&Record{Block: "dropped"})

	path := filepath.Join(t.TempDir(), "journal.jsonl")
	SetPath(path)
	if !Enabled() {
		t.Fatalf("a path turns the journal on")
	}
	Write(&Record{Block: "block", HeaderCommitment: "h", ResultMultiset: "r"})
	Write(&Record{Block: "template", ResultMultiset: "r"})
	SetPath("")

	records, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].Block != "block" || records[1].Block != "template" ||
		records[0].RunID == "" || records[0].Time == "" {
		t.Errorf("expected both records in order, stamped with run and time; got %+v", records)
	}
}
