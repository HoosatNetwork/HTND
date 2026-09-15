package consensusstatemanager_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus"
	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/testapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/processes/consensusstatemanager"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/multiset"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/testutils"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/utxo"
	"github.com/HoosatNetwork/HTND/util/staging"
)

var errSimulatedKill = errors.New("simulated kill")

// TestReverseUTXODiffsInterruptedMidway is the HTN-005 crash repro for a node killed in the middle of ReverseUTXODiffs.
// That function rewrites the resolved chain's UTXO diffs one block per commit, after the statuses were already
// committed, so on restart the blocks are Valid, ResolveBlockStatus returns no reversal data, and the rest of the
// reversal never runs. For every kill point it checks what the retried resolution leaves behind: virtual's UTXO table
// against virtual's multiset, and every chain block's past rebuilt from the diff chain against its header commitment.
func TestReverseUTXODiffsInterruptedMidway(t *testing.T) {
	for killAfter := 1; killAfter <= 12; killAfter++ {
		completed := false
		t.Run(fmt.Sprintf("kill-after-%d-diffs", killAfter), func(t *testing.T) {
			completed = interruptedReversal(t, killAfter)
		})
		if completed {
			t.Logf("the reversal finishes in fewer than %d commits; every earlier kill point was tested", killAfter)
			break
		}
	}
}

func interruptedReversal(t *testing.T, killAfter int) (reversalCompletedBeforeKill bool) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		tc, teardown, err := consensus.NewFactory().NewTestConsensus(consensusConfig, "TestReverseUTXODiffsInterruptedMidway")
		if err != nil {
			t.Fatalf("NewTestConsensus: %+v", err)
		}
		defer teardown(false)

		tip := consensusConfig.GenesisHash
		var chain []*externalapi.DomainHash
		for range 6 {
			tip, _, err = tc.AddBlock([]*externalapi.DomainHash{tip}, nil, nil)
			if err != nil {
				t.Fatalf("AddBlock: %+v", err)
			}
			chain = append(chain, tip)
		}
		// A sibling tip merged by virtual, so virtual's DAA score runs ahead of its selected parent's.
		if _, _, err := tc.AddBlock([]*externalapi.DomainHash{chain[len(chain)-2]}, nil, nil); err != nil {
			t.Fatalf("AddBlock sibling: %+v", err)
		}
		tip, err = tc.GetVirtualSelectedParent()
		if err != nil {
			t.Fatalf("GetVirtualSelectedParent: %+v", err)
		}
		for range 5 {
			block, _, err := tc.BuildBlockWithParents([]*externalapi.DomainHash{tip}, nil, nil)
			if err != nil {
				t.Fatalf("BuildBlockWithParents: %+v", err)
			}
			if err := tc.ValidateAndInsertBlock(block, false, true); err != nil {
				t.Fatalf("ValidateAndInsertBlock: %+v", err)
			}
			tip = consensushashing.BlockHash(block)
			chain = append(chain, tip)
		}

		stagingArea := model.NewStagingArea()
		status, reversalData, err := tc.ConsensusStateManager().ResolveBlockStatus(stagingArea, tip, false)
		if err != nil {
			t.Fatalf("ResolveBlockStatus: %+v", err)
		}
		if status != externalapi.StatusUTXOValid || reversalData == nil {
			t.Fatalf("setup: expected a valid resolution with reversal data, got %s", status)
		}
		if err := staging.CommitAllChanges(tc.DatabaseContext(), stagingArea); err != nil {
			t.Fatalf("CommitAllChanges: %+v", err)
		}

		restore := consensusstatemanager.SetReverseUTXODiffsInterruptHook(func(committedDiffs int) error {
			if committedDiffs == killAfter {
				return errSimulatedKill
			}
			return nil
		})
		err = tc.ConsensusStateManager().ReverseUTXODiffs(tip, reversalData)
		restore()
		switch {
		case err == nil:
			reversalCompletedBeforeKill = true
			return
		case !errors.Is(err, errSimulatedKill):
			t.Fatalf("ReverseUTXODiffs: %+v", err)
		}

		// The restart.
		if err := tc.ResolveVirtual(nil); err != nil {
			t.Errorf("ResolveVirtual after a kill after %d reversed diffs: %+v", killAfter, err)
			return
		}
		virtualSelectedParent, err := tc.GetVirtualSelectedParent()
		if err != nil {
			t.Fatalf("GetVirtualSelectedParent: %+v", err)
		}
		if !virtualSelectedParent.Equal(tip) {
			t.Errorf("after the retry virtual's selected parent is %s, not the resolved tip %s", virtualSelectedParent, tip)
		}

		tableHash, err := virtualTableHash(tc)
		if err != nil {
			t.Fatalf("virtual table: %+v", err)
		}
		virtualMultiset, err := tc.MultisetStore().Get(tc.DatabaseContext(), model.NewStagingArea(), model.VirtualBlockHash)
		if err != nil {
			t.Fatalf("virtual multiset: %+v", err)
		}
		if !tableHash.Equal(virtualMultiset.Hash()) {
			t.Errorf("kill after %d reversed diffs: virtual UTXO table %s != virtual multiset %s", killAfter, tableHash, virtualMultiset.Hash())
		}
		for _, blockHash := range chain {
			header, err := tc.GetBlockHeader(blockHash)
			if err != nil {
				t.Fatalf("GetBlockHeader: %+v", err)
			}
			restored, err := restoredPastHash(tc, blockHash)
			if err != nil {
				t.Errorf("kill after %d reversed diffs: restoring %s's past failed: %+v", killAfter, blockHash, err)
				continue
			}
			if !restored.Equal(header.UTXOCommitment()) {
				t.Errorf("kill after %d reversed diffs: %s's past rebuilds to %s but its header commits to %s",
					killAfter, blockHash, restored, header.UTXOCommitment())
			}
		}
	})
	return reversalCompletedBeforeKill
}

func hashUTXOSet(iterator externalapi.ReadOnlyUTXOSetIterator) (*externalapi.DomainHash, error) {
	defer iterator.Close()
	hashed := multiset.New()
	for ok := iterator.First(); ok; ok = iterator.Next() {
		outpoint, entry, err := iterator.Get()
		if err != nil {
			return nil, err
		}
		serialized, err := utxo.SerializeUTXO(entry, outpoint)
		if err != nil {
			return nil, err
		}
		hashed.Add(serialized)
	}
	return hashed.Hash(), nil
}

func virtualTableHash(tc testapi.TestConsensus) (*externalapi.DomainHash, error) {
	iterator, err := tc.ConsensusStateStore().VirtualUTXOSetIterator(tc.DatabaseContext(), model.NewStagingArea())
	if err != nil {
		return nil, err
	}
	return hashUTXOSet(iterator)
}

func restoredPastHash(tc testapi.TestConsensus, blockHash *externalapi.DomainHash) (*externalapi.DomainHash, error) {
	iterator, err := tc.ConsensusStateManager().RestorePastUTXOSetIterator(model.NewStagingArea(), blockHash)
	if err != nil {
		return nil, err
	}
	return hashUTXOSet(iterator)
}
