package consensus_test

import (
	"testing"

	"github.com/pkg/errors"

	"github.com/HoosatNetwork/HTND/domain/consensus"
	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/ruleerrors"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/blockheader"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/hardforks"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/testutils"
)

// Workstream C's central safety property, tested from the outside: each dormant consensus rule must
// change nothing while its gate is unscheduled, and must actually bite once it is scheduled.
//
// Both halves matter, and the first is the one that protects the live network. A rule that fires
// early rejects history - the chain was built without it - so a node applying it would disqualify
// its own chain and leave the network alone. A rule that never fires even when scheduled is a
// different failure: everyone believes a fork shipped and nothing changed.

// withWrongBits returns a copy of block whose header claims a different difficulty, leaving every
// other field alone. The block hash changes as a result, which is fine: these tests run with proof
// of work skipped, and what is under test is the bits comparison rather than the work.
func withWrongBits(block *externalapi.DomainBlock, bits uint32) *externalapi.DomainBlock {
	header := block.Header
	return &externalapi.DomainBlock{
		Header: blockheader.NewImmutableBlockHeader(
			header.Version(),
			header.Parents(),
			header.HashMerkleRoot(),
			header.AcceptedIDMerkleRoot(),
			header.UTXOCommitment(),
			header.TimeInMilliseconds(),
			bits,
			header.Nonce(),
			header.DAAScore(),
			header.BlueScore(),
			header.BlueWork(),
			header.PruningPoint(),
		),
		Transactions: block.Transactions,
	}
}

// TestHeaderBitsRuleIsInertUntilItsGateIsScheduled is HTN-007's admissibility half: a block whose
// bits are wrong - which is every block, as far as this rule is concerned, because nothing has ever
// checked them - must still be accepted while the gate is unscheduled.
//
// This is not hypothetical for HTN-007 specifically. HTN-221 changed the retarget formula with no
// version gate, on the explicit reasoning that bits are never strictly validated. So blocks built
// before and after that change disagree about the correct bits for the same window, and enforcing
// this rule on existing versions would reject blocks this very node would have built.
func TestHeaderBitsRuleIsInertUntilItsGateIsScheduled(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		tc, teardown, err := consensus.NewFactory().NewTestConsensus(consensusConfig,
			"TestHeaderBitsRuleIsInertUntilItsGateIsScheduled")
		if err != nil {
			t.Fatalf("NewTestConsensus: %+v", err)
		}
		defer teardown(false)

		if hardforks.IsScheduled(hardforks.ValidateHeaderBitsVersion) {
			t.Skip("ValidateHeaderBitsVersion has been scheduled; this test covers the dormant state")
		}

		block, _, err := tc.BuildBlockWithParents(
			[]*externalapi.DomainHash{consensusConfig.GenesisHash}, nil, nil)
		if err != nil {
			t.Fatalf("BuildBlockWithParents: %+v", err)
		}

		// Deliberately nonsense bits, far from anything the window could produce.
		tampered := withWrongBits(block, block.Header.Bits()-1)
		if err := tc.ValidateAndInsertBlock(tampered, true, true); err != nil {
			t.Fatalf("a block with wrong difficulty bits was rejected while the rule is dormant, so "+
				"this change is NOT inert and would reject existing history: %+v", err)
		}

		status, err := tc.BlockStatusStore().Get(tc.DatabaseContext(), model.NewStagingArea(),
			consensushashing.BlockHash(tampered))
		if err != nil {
			t.Fatalf("reading the block status: %+v", err)
		}
		if status == externalapi.StatusInvalid || status == externalapi.StatusDisqualifiedFromChain {
			t.Fatalf("a block with wrong bits landed as %s while the rule is dormant", status)
		}
	})
}

// TestHeaderBitsRuleRejectsWrongBitsOnceScheduled is the other half: once the gate names a version
// the block actually reaches, the same block is refused with ErrUnexpectedDifficulty.
//
// Without this, HTN-007's fix would be untestable by construction - the rule is unreachable on
// purpose - and an unexecuted rule is a draft, not an implementation.
func TestHeaderBitsRuleRejectsWrongBitsOnceScheduled(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		tc, teardown, err := consensus.NewFactory().NewTestConsensus(consensusConfig,
			"TestHeaderBitsRuleRejectsWrongBitsOnceScheduled")
		if err != nil {
			t.Fatalf("NewTestConsensus: %+v", err)
		}
		defer teardown(false)

		// Version 1 covers everything from genesis, so scheduling there activates the rule for the
		// blocks this test builds.
		defer hardforks.SetForTest(&hardforks.ValidateHeaderBitsVersion, 1)()

		block, _, err := tc.BuildBlockWithParents(
			[]*externalapi.DomainHash{consensusConfig.GenesisHash}, nil, nil)
		if err != nil {
			t.Fatalf("BuildBlockWithParents: %+v", err)
		}

		// The untouched block must still be accepted: the rule compares against what this node
		// itself computes, so a block this node built has to satisfy it. If this fails, the rule is
		// not "strict", it is broken.
		if err := tc.ValidateAndInsertBlock(block, true, true); err != nil {
			t.Fatalf("a block built by this very node was rejected by its own difficulty rule: %+v", err)
		}

		tampered := withWrongBits(block, block.Header.Bits()-1)
		err = tc.ValidateAndInsertBlock(tampered, true, true)
		if err == nil {
			t.Fatal("a block claiming the wrong difficulty was accepted although the rule is active")
		}
		if !errors.Is(err, ruleerrors.ErrUnexpectedDifficulty) {
			t.Fatalf("expected ErrUnexpectedDifficulty, got: %+v", err)
		}
	})
}

// TestEveryGateIsUnscheduledInAShippedBuild is the belt-and-braces check against the one mistake
// this whole design is built to prevent: shipping with a gate accidentally left scheduled.
//
// hardforks' own tests assert the same thing, but they can only see the package's values. This runs
// from the consensus package, after all of its init work, so it also catches anything that assigned
// to a gate on the way here.
func TestEveryGateIsUnscheduledInAShippedBuild(t *testing.T) {
	for name, gate := range map[string]uint16{
		"StrictUTXOCommitmentVersion":   hardforks.StrictUTXOCommitmentVersion,
		"RefuseMismatchedImportVersion": hardforks.RefuseMismatchedImportVersion,
		"ValidateHeaderBitsVersion":     hardforks.ValidateHeaderBitsVersion,
		"ValidateIBDPruningListVersion": hardforks.ValidateIBDPruningListVersion,
	} {
		if hardforks.IsScheduled(gate) {
			t.Errorf("%s is scheduled at version %d in this build. No gate may be scheduled without "+
				"a matching POWScores entry and a lockstep extension of every per-version parameter "+
				"table - see the hardforks package comment.", name, gate)
		}
	}
}
