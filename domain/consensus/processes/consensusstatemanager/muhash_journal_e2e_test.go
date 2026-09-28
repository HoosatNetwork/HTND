package consensusstatemanager_test

import (
	"path/filepath"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/muhashjournal"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/testutils"
)

// TestMuHashJournalRecordsWhatWasHashed pins that the journal is a faithful account of the real
// multiset computation, which is the only thing that makes an analysis of it mean anything: every
// record replays from its parent state to its recorded result, every validated block's record
// reproduces the header it was mined with, and the block builder's template record - what a miner
// journals - pairs with the validated block by result and carries exactly the same elements.
func TestMuHashJournalRecordsWhatWasHashed(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		path := filepath.Join(t.TempDir(), "muhash.jsonl")
		muhashjournal.SetPath(path)
		defer muhashjournal.SetPath("")

		tc, teardown, err := consensus.NewFactory().NewTestConsensus(consensusConfig, "TestMuHashJournalRecordsWhatWasHashed")
		if err != nil {
			t.Fatalf("NewTestConsensus: %+v", err)
		}
		defer teardown(false)

		// A chain with a side block merged into it, so some records merge more than one block.
		tip := consensusConfig.GenesisHash
		var side *externalapi.DomainHash
		for i := 0; i < 6; i++ {
			parents := []*externalapi.DomainHash{tip}
			if i == 3 && side != nil {
				parents = append(parents, side)
			}
			tip, _, err = tc.AddBlock(parents, nil, nil)
			if err != nil {
				t.Fatalf("AddBlock: %+v", err)
			}
			if i == 1 {
				side, _, err = tc.AddBlock([]*externalapi.DomainHash{tip}, nil, nil)
				if err != nil {
					t.Fatalf("AddBlock: %+v", err)
				}
			}
		}
		muhashjournal.SetPath("")

		records, err := muhashjournal.Read(path)
		if err != nil {
			t.Fatalf("Read: %+v", err)
		}
		var blocks, templates []*muhashjournal.Record
		for _, record := range records {
			if err := record.Verify(); err != nil {
				t.Errorf("%s record %s does not replay: %s", record.Kind, record.Block, err)
			}
			switch record.Kind {
			case muhashjournal.KindBlock:
				blocks = append(blocks, record)
				if !record.MatchesHeader() {
					t.Errorf("block %s: result %s does not reproduce its header %s", record.Block,
						record.ResultMultiset, record.HeaderCommitment)
				}
			case muhashjournal.KindTemplate:
				templates = append(templates, record)
			}
		}
		if len(blocks) == 0 || len(templates) == 0 {
			t.Fatalf("expected both block and template records, got %d and %d of %d", len(blocks), len(templates),
				len(records))
		}

		merged := false
		for _, pair := range muhashjournal.Match(blocks, templates) {
			if pair.How != "result" {
				continue
			}
			if difference := muhashjournal.Compare(pair); !difference.Empty() {
				t.Errorf("block %s and the template that built it hashed different elements: %+v",
					pair.A.Block, difference)
			}
			if len(pair.A.MergeSet) > 1 {
				merged = true
			}
		}
		if !merged {
			t.Errorf("expected a paired record merging more than one block")
		}
	})
}
