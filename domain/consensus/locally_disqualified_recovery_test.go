package consensus_test

import (
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus"
	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/testapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/testutils"
	"github.com/HoosatNetwork/HTND/util/staging"
	"github.com/pkg/errors"
)

func isNoUsableTip(err error) bool {
	return errors.Is(err, externalapi.ErrVirtualHasNoUsableTip)
}

// buildChainForRecovery builds a chain of length blocks on a builder consensus and inserts it into a
// fresh one: the first resolvedPrefix blocks with virtual updated, the rest left pending, the shape
// IBD leaves a node in before it resolves virtual.
func buildChainForRecovery(t *testing.T, consensusConfig *consensus.Config, name string, length, resolvedPrefix int,
) (tc testapi.TestConsensus, builder testapi.TestConsensus, chain []*externalapi.DomainHash, teardown func()) {
	t.Helper()
	factory := consensus.NewFactory()
	builder, teardownBuilder, err := factory.NewTestConsensus(consensusConfig, name+"_builder")
	if err != nil {
		t.Fatalf("NewTestConsensus: %+v", err)
	}
	tip := consensusConfig.GenesisHash
	for i := 0; i < length; i++ {
		tip, _, err = builder.AddBlock([]*externalapi.DomainHash{tip}, nil, nil)
		if err != nil {
			t.Fatalf("AddBlock %d: %+v", i, err)
		}
		chain = append(chain, tip)
	}
	tc, teardownTC, err := factory.NewTestConsensus(consensusConfig, name)
	if err != nil {
		t.Fatalf("NewTestConsensus: %+v", err)
	}
	for i, hash := range chain {
		block, found, err := builder.GetBlock(hash)
		if err != nil || !found {
			t.Fatalf("GetBlock %d: found=%t err=%+v", i, found, err)
		}
		if err := tc.ValidateAndInsertBlock(block, i < resolvedPrefix, true); err != nil {
			t.Fatalf("ValidateAndInsertBlock %d: %+v", i, err)
		}
	}
	return tc, builder, chain, func() {
		teardownTC(false)
		teardownBuilder(false)
	}
}

func stageStatus(t *testing.T, tc testapi.TestConsensus, status externalapi.BlockStatus, hashes ...*externalapi.DomainHash) {
	t.Helper()
	stagingArea := model.NewStagingArea()
	for _, hash := range hashes {
		tc.BlockStatusStore().Stage(stagingArea, hash, status)
	}
	if err := staging.CommitAllChanges(tc.DatabaseContext(), stagingArea); err != nil {
		t.Fatalf("CommitAllChanges: %+v", err)
	}
}

func statusOf(t *testing.T, tc testapi.TestConsensus, hash *externalapi.DomainHash) externalapi.BlockStatus {
	t.Helper()
	status, err := tc.BlockStatusStore().Get(tc.DatabaseContext(), model.NewStagingArea(), hash)
	if err != nil {
		t.Fatalf("Get status: %+v", err)
	}
	return status
}

// TestRepairReachesDisqualifiedSegmentBelowPendingTip pins that IBD's status repair reaches a
// locally disqualified block that sits below pending blocks - the shape IBD itself leaves behind:
// bodies synced above a hash this node had already disqualified, not yet resolved.
//
// The repair stopped at the first block that was not disqualified, which is the pending tip, so it
// reset nothing. IBD read "nothing reset" as "the tips are invalid, repairing cannot help", and the
// next resolve cascade-disqualified every synced block from the unreset hash up. Every IBD round
// ended in the same place, against every peer.
func TestRepairReachesDisqualifiedSegmentBelowPendingTip(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		tc, _, chain, teardown := buildChainForRecovery(t, consensusConfig,
			"TestRepairReachesDisqualifiedSegmentBelowPendingTip", 6, 2)
		defer teardown()

		// A disqualification this node holds for chain[2], below the synced-but-pending bodies.
		stageStatus(t, tc, externalapi.StatusDisqualifiedFromChain, chain[2])

		reset, err := tc.RepairDisqualifiedTipChains()
		if err != nil {
			t.Fatalf("RepairDisqualifiedTipChains: %+v", err)
		}
		if reset != 1 {
			t.Fatalf("expected the disqualified block under the pending tip to be reset, %d blocks were", reset)
		}
		if status := statusOf(t, tc, chain[1]); status != externalapi.StatusUTXOValid {
			t.Fatalf("the walk must stop at the first UTXO-valid block, which is now %s", status)
		}

		if err := tc.ResolveVirtual(nil); err != nil {
			t.Fatalf("ResolveVirtual after the repair: %+v", err)
		}
		for i, hash := range chain {
			if status := statusOf(t, tc, hash); status != externalapi.StatusUTXOValid {
				t.Fatalf("block %d did not re-verify as valid after the repair: %s", i, status)
			}
		}
	})
}

// TestRepairDoesNotResetVirtualSelectedParentFromBelow pins the guard that keeps the repair from
// crashing the node.
//
// Virtual's selected parent holds the UTXO diff relative to virtual that every other restore path
// ends in. Reset and re-resolved as a non-tip block of a longer chain, it was given a temporary diff
// pointing at its selected parent - whose diff still pointed back at it - and restorePastUTXO then
// walked that cycle until the process ran out of memory. The base commit reached this with a
// disqualified tip above a disqualified virtual selected parent; walking through pending blocks
// would have made it routine. On a verified baseline the chain above it is then disqualified again,
// which is the correct verdict there, and reported as ErrVirtualHasNoUsableTip.
func TestRepairDoesNotResetVirtualSelectedParentFromBelow(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		// Virtual on chain[3], chain[4] and chain[5] pending above it.
		tc, _, chain, teardown := buildChainForRecovery(t, consensusConfig,
			"TestRepairDoesNotResetVirtualSelectedParentFromBelow", 6, 4)
		defer teardown()
		stageStatus(t, tc, externalapi.StatusDisqualifiedFromChain, chain[3], chain[4], chain[5])

		reset, err := tc.RepairDisqualifiedTipChains()
		if err != nil {
			t.Fatalf("RepairDisqualifiedTipChains: %+v", err)
		}
		if reset != 2 {
			t.Fatalf("expected the two disqualified blocks above virtual's selected parent to be reset, got %d", reset)
		}
		if status := statusOf(t, tc, chain[3]); status != externalapi.StatusDisqualifiedFromChain {
			t.Fatalf("virtual's selected parent must not be reset from below, it is %s", status)
		}

		err = tc.ResolveVirtual(nil)
		if !isNoUsableTip(err) {
			t.Fatalf("expected the chain to cascade again on a verified baseline and be reported as "+
				"ErrVirtualHasNoUsableTip, got %+v", err)
		}
	})
}

// TestBlockIsInsertedWhenVirtualHasNoUsableTip pins that "no pending tip" reaches callers as
// ErrVirtualHasNoUsableTip, and that the node still accepts new blocks in that state.
//
// findNextPendingTip reported it as a plain error. consensus.ResolveVirtual returned that before its
// own ErrVirtualHasNoUsableTip check, so IBD's status repair, which keys on the sentinel, never ran
// in exactly the state it was written for; and ValidateAndInsertBlock returned it for every block
// while virtual was marked not-updated, refusing the very blocks that could give it a usable tip.
func TestBlockIsInsertedWhenVirtualHasNoUsableTip(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		tc, builder, chain, teardown := buildChainForRecovery(t, consensusConfig,
			"TestBlockIsInsertedWhenVirtualHasNoUsableTip", 3, 3)
		defer teardown()

		// A side block arrives without virtual being updated, which leaves virtual marked
		// not-updated, the state in which every insertion first drains virtual resolution.
		sideHash, _, err := builder.AddBlock([]*externalapi.DomainHash{chain[0]}, nil, nil)
		if err != nil {
			t.Fatalf("AddBlock side: %+v", err)
		}
		side, _, err := builder.GetBlock(sideHash)
		if err != nil {
			t.Fatalf("GetBlock side: %+v", err)
		}
		if err := tc.ValidateAndInsertBlock(side, false, true); err != nil {
			t.Fatalf("ValidateAndInsertBlock side: %+v", err)
		}

		// Both tips are disqualified, so nothing is left to resolve. findNextPendingTip then falls back
		// to walking the headers selected chain, which on a pruned node runs into blocks this node
		// holds no status for; a headers selected tip with no stored status stands in for that.
		stageStatus(t, tc, externalapi.StatusDisqualifiedFromChain, chain[2], sideHash)
		stagingArea := model.NewStagingArea()
		tc.HeaderTipsStore().Stage(stagingArea, externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{0xde, 0xad}))
		if err := staging.CommitAllChanges(tc.DatabaseContext(), stagingArea); err != nil {
			t.Fatalf("CommitAllChanges: %+v", err)
		}

		err = tc.ResolveVirtual(nil)
		if !isNoUsableTip(err) {
			t.Fatalf("expected ErrVirtualHasNoUsableTip, so IBD's repair runs, got %+v", err)
		}

		// Put the real headers selected tip back: the stand-in above has no header, and block
		// insertion reads it. The insertion below then checks only that the node still accepts
		// blocks after the typed error; the no-usable-tip branch of ValidateAndInsertBlock itself
		// needs a pruned DAG to reach.
		stagingArea = model.NewStagingArea()
		tc.HeaderTipsStore().Stage(stagingArea, chain[2])
		if err := staging.CommitAllChanges(tc.DatabaseContext(), stagingArea); err != nil {
			t.Fatalf("CommitAllChanges: %+v", err)
		}

		// A new block from the network arrives.
		nextHash, _, err := builder.AddBlock([]*externalapi.DomainHash{chain[2]}, nil, nil)
		if err != nil {
			t.Fatalf("AddBlock: %+v", err)
		}
		next, _, err := builder.GetBlock(nextHash)
		if err != nil {
			t.Fatalf("GetBlock: %+v", err)
		}
		if err := tc.ValidateAndInsertBlock(next, true, true); err != nil {
			t.Fatalf("a block was refused because virtual had no usable tip: %+v", err)
		}
		if _, err := tc.GetBlockInfo(nextHash); err != nil {
			t.Fatalf("GetBlockInfo: %+v", err)
		}
	})
}
