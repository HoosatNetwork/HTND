package consensus_test

import (
	"testing"

	"github.com/pkg/errors"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/ruleerrors"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/blockheader"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/testutils"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
)

// Workstream C's central safety property, tested from the outside: each gated consensus rule must
// change nothing below its activation version, and must actually bite once blocks reach it.
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

// TestHeaderBitsRuleIsInertBelowItsActivationVersion is HTN-007's admissibility half: a block whose
// bits are wrong - which is every block, as far as this rule is concerned, because nothing has ever
// checked them - must still be accepted below dagconfig.ValidateHeaderBitsVersion.
//
// This is not hypothetical for HTN-007 specifically. HTN-221 changed the retarget formula with no
// version gate, on the explicit reasoning that bits are never strictly validated. So blocks built
// before and after that change disagree about the correct bits for the same window, and enforcing
// this rule on existing versions would reject blocks this very node would have built.
func TestHeaderBitsRuleIsInertBelowItsActivationVersion(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		tc, teardown, err := consensus.NewFactory().NewTestConsensus(consensusConfig,
			"TestHeaderBitsRuleIsInertBelowItsActivationVersion")
		if err != nil {
			t.Fatalf("NewTestConsensus: %+v", err)
		}
		defer teardown(false)

		block, _, err := tc.BuildBlockWithParents(
			[]*externalapi.DomainHash{consensusConfig.GenesisHash}, nil, nil)
		if err != nil {
			t.Fatalf("BuildBlockWithParents: %+v", err)
		}

		// Deliberately nonsense bits, far from anything the window could produce.
		tampered := withWrongBits(block, block.Header.Bits()-1)
		if err := tc.ValidateAndInsertBlock(tampered, true, true); err != nil {
			t.Fatalf("a block with wrong difficulty bits was rejected below the activation version, so "+
				"this change is NOT inert and would reject existing history: %+v", err)
		}

		status, err := tc.BlockStatusStore().Get(tc.DatabaseContext(), model.NewStagingArea(),
			consensushashing.BlockHash(tampered))
		if err != nil {
			t.Fatalf("reading the block status: %+v", err)
		}
		if status == externalapi.StatusInvalid || status == externalapi.StatusDisqualifiedFromChain {
			t.Fatalf("a block with wrong bits landed as %s below the activation version", status)
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
		defer func(previous uint16) { dagconfig.ValidateHeaderBitsVersion = previous }(
			dagconfig.ValidateHeaderBitsVersion)
		dagconfig.ValidateHeaderBitsVersion = 1

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

// TestNoGateIsReachableOnAnyNetwork is the belt-and-braces check against shipping a gated rule that a
// network can already reach. A block version is reachable when its POWScores activation score is
// not the ^uint64(0) placeholder. It runs from the consensus package, after all of its init work, so
// it also catches anything that assigned to a gate on the way here.
func TestNoGateIsReachableOnAnyNetwork(t *testing.T) {
	for _, params := range []*dagconfig.Params{&dagconfig.MainnetParams, &dagconfig.TestnetParams,
		&dagconfig.TestnetParamsB5, &dagconfig.TestnetParamsB10, &dagconfig.SimnetParams, &dagconfig.DevnetParams} {
		highestReachable := uint16(1)
		for _, score := range params.POWScores {
			if score != ^uint64(0) {
				highestReachable++
			}
		}
		for name, gate := range map[string]uint16{
			"StrictUTXOCommitmentVersion":   dagconfig.StrictUTXOCommitmentVersion,
			"StrictMinersViewFieldsVersion": dagconfig.StrictMinersViewFieldsVersion,
			"RefuseMismatchedImportVersion": dagconfig.RefuseMismatchedImportVersion,
			"ValidateHeaderBitsVersion":     dagconfig.ValidateHeaderBitsVersion,
			"ValidateIBDPruningListVersion": dagconfig.ValidateIBDPruningListVersion,
			"OffsetModeValueChecksVersion":  dagconfig.OffsetModeValueChecksVersion,
		} {
			if dagconfig.HardForkActive(gate, highestReachable) {
				t.Errorf("%s: %s (block version %d) is reachable at block version %d. Give it a real "+
					"POWScores activation score only as a coordinated hard fork.",
					params.Name, name, gate, highestReachable)
			}
		}
	}
}
