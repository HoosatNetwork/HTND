package hardforks

import (
	"math"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
)

// gates is every activation constant this package defines, by name, so a new gate added without a
// test is a compile-time-obvious omission rather than a silent one.
func gates() map[string]uint16 {
	return map[string]uint16{
		"StrictUTXOCommitmentVersion":   StrictUTXOCommitmentVersion,
		"RefuseMismatchedImportVersion": RefuseMismatchedImportVersion,
		"ValidateHeaderBitsVersion":     ValidateHeaderBitsVersion,
		"ValidateIBDPruningListVersion": ValidateIBDPruningListVersion,
	}
}

func networks() map[string]*dagconfig.Params {
	return map[string]*dagconfig.Params{
		"mainnet": &dagconfig.MainnetParams,
		"testnet": &dagconfig.TestnetParams,
		"simnet":  &dagconfig.SimnetParams,
		"devnet":  &dagconfig.DevnetParams,
	}
}

// highestReachableVersion is the largest block version a network can currently produce. POWScores
// defines one version per entry plus version 1, which is pre-activation - the same arithmetic
// constants.BlockVersionForDAAScore performs.
func highestReachableVersion(params *dagconfig.Params) int {
	return len(params.POWScores) + 1
}

// TestUnscheduledGatesAreInertForEveryReachableVersion is the property the whole workstream rests
// on: none of these rules may change the verdict on any block any network can produce today.
//
// Every rule gated here is a check consensus currently does not perform. Turning one on for an
// existing block version would reject history - the chain was built without it - so a node applying
// it would disqualify its own chain and leave the network alone. This test is what stands between a
// one-character edit to a constant and that outcome.
func TestUnscheduledGatesAreInertForEveryReachableVersion(t *testing.T) {
	for networkName, params := range networks() {
		for gateName, activationVersion := range gates() {
			if IsScheduled(activationVersion) {
				// Activation is a deliberate act; the test below covers that case.
				continue
			}
			// 0 and 1 are included on purpose: 0 should never occur, and 1 is what the
			// process-global version reads on a freshly started node before any block arrives.
			for blockVersion := 0; blockVersion <= highestReachableVersion(params); blockVersion++ {
				if Active(activationVersion, uint16(blockVersion)) {
					t.Errorf("%s: %s is active for block version %d, which %s can already produce. "+
						"An unscheduled gate must never fire: this would apply a new consensus rule "+
						"to blocks that already exist.",
						networkName, gateName, blockVersion, networkName)
				}
			}
		}
	}
}

// TestUnscheduledGatesAreInertEvenAtAnAbsurdVersion covers the other way a gate could leak: not a
// block claiming a high version, but this node's own process-global version being driven up.
//
// constants.GetBlockVersion is a process-global that only ratchets upward as blocks arrive, and
// several code paths fall back to it when a DAA score is unavailable (genesis, trusted-data
// bootstrap). So "no block carries this version" is not on its own enough - the gate has to be off
// for any value a fallback could hand it.
func TestUnscheduledGatesAreInertEvenAtAnAbsurdVersion(t *testing.T) {
	for gateName, activationVersion := range gates() {
		if IsScheduled(activationVersion) {
			continue
		}
		for _, blockVersion := range []uint16{0, 1, 10, 11, 255, 1000, math.MaxUint16 - 1, math.MaxUint16} {
			if Active(activationVersion, blockVersion) {
				t.Errorf("%s is active at block version %d although it is unscheduled",
					gateName, blockVersion)
			}
		}
	}
}

// TestTheUnscheduledPlaceholderIsUnreachable is the loud failure the plan asked for.
//
// The placeholder is only inert because no network can reach it. If a future fork extends POWScores
// far enough that a real block could carry math.MaxUint16 as its version, every dormant gate in
// this package would activate at once, unannounced. That is implausible - it would need tens of
// thousands of entries - but the whole point of a dormant consensus rule is that nothing activates
// it by accident, so it is asserted rather than assumed.
func TestTheUnscheduledPlaceholderIsUnreachable(t *testing.T) {
	for networkName, params := range networks() {
		if highest := highestReachableVersion(params); uint16(highest) >= unscheduledActivation {
			t.Fatalf("%s can reach block version %d, at or above the unscheduled placeholder %d. "+
				"Every dormant gate in this package would now be live. Give each gate a real "+
				"activation version, or raise the placeholder.",
				networkName, highest, unscheduledActivation)
		}
	}
}

// TestAScheduledGateIsDefinedByPOWScores fires at activation time, not before.
//
// A gate set to a version no POWScores entry defines is worse than one left unscheduled: it looks
// activated in the diff and in review, but no block ever reaches that version, so the rule silently
// never runs. That is the failure mode where everyone believes a fork shipped and it did not.
//
// It only checks mainnet. Test networks legitimately run ahead of or behind mainnet's activation
// schedule, so requiring every network to define the version would block staging a fork on testnet
// first - which is exactly how one should be staged.
func TestAScheduledGateIsDefinedByPOWScores(t *testing.T) {
	mainnet := &dagconfig.MainnetParams
	highest := highestReachableVersion(mainnet)

	for gateName, activationVersion := range gates() {
		if !IsScheduled(activationVersion) {
			continue
		}
		if int(activationVersion) > highest {
			t.Errorf("%s is scheduled at block version %d, but mainnet's POWScores only defines "+
				"versions up to %d. No block will ever reach it, so the rule would never run "+
				"despite looking activated. Add the activation DAA score to POWScores and extend "+
				"every per-version table in lockstep.",
				gateName, activationVersion, highest)
		}
		if activationVersion < 2 {
			t.Errorf("%s is scheduled at block version %d. Version 1 is pre-activation and covers "+
				"the whole of early history, so gating there applies the rule retroactively.",
				gateName, activationVersion)
		}
	}
}

// TestActiveComparesFromTheActivationVersionOnward pins the comparison itself, independently of
// whether anything is currently scheduled - otherwise the tests above would all pass against an
// Active that simply returned false.
func TestActiveComparesFromTheActivationVersionOnward(t *testing.T) {
	const activation uint16 = 12

	for _, testCase := range []struct {
		blockVersion uint16
		wantActive   bool
	}{
		{1, false},
		{11, false},
		{12, true}, // the activation version itself is included
		{13, true},
		{math.MaxUint16 - 1, true},
	} {
		if got := Active(activation, testCase.blockVersion); got != testCase.wantActive {
			t.Errorf("Active(%d, %d) = %v, want %v",
				activation, testCase.blockVersion, got, testCase.wantActive)
		}
	}

	if !IsScheduled(activation) {
		t.Error("IsScheduled reported a real version as unscheduled")
	}
	if IsScheduled(unscheduledActivation) {
		t.Error("IsScheduled reported the placeholder as scheduled")
	}
}

// TestNoGateIsReachableFromTheCurrentGlobalVersion ties the guarantee to the version arithmetic the
// rest of consensus actually uses, rather than to this package's own idea of what is reachable.
func TestNoGateIsReachableFromTheCurrentGlobalVersion(t *testing.T) {
	mainnet := &dagconfig.MainnetParams

	// The version mainnet is on today, derived the way every consensus manager derives it.
	for _, daaScore := range append([]uint64{0, 1}, mainnet.POWScores...) {
		blockVersion := constants.BlockVersionForDAAScore(mainnet.POWScores, daaScore)
		for gateName, activationVersion := range gates() {
			if IsScheduled(activationVersion) {
				continue
			}
			if Active(activationVersion, blockVersion) {
				t.Errorf("%s is active for a block at mainnet DAA score %d (block version %d)",
					gateName, daaScore, blockVersion)
			}
		}
	}
}
