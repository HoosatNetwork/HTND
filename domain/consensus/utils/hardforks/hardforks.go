// Package hardforks holds the activation version of each consensus rule that is implemented but not
// yet scheduled, and the one predicate that decides whether such a rule applies to a given block.
//
// Every rule here is OFF for every block version that any network can currently produce. That is
// not a side effect of the current values - it is the property this package exists to guarantee,
// and hardforks_test.go asserts it against every registered network's POWScores table.
//
// # Why the gates exist at all
//
// Each of these rules is a check that consensus is supposed to perform and currently does not.
// Turning any of them on for existing block versions would reject history: the chain that exists
// today was built without them, so blocks that are valid now would become invalid, and a node
// applying the rule would disqualify its own chain and fall off the network alone. The rules are
// therefore written, tested and shipped dormant, so that activating them later is a matter of
// choosing a version and a DAA score rather than writing consensus code under time pressure during
// an incident.
//
// # How to activate one
//
// This is a release-captain task, not something to do while fixing a bug:
//
//  1. Add the activation DAA score as a new entry in the network's POWScores, which defines a new
//     block version (a network's highest version is len(POWScores)+1).
//  2. Extend EVERY per-version parameter table in lockstep - K, TargetTimePerBlock,
//     FinalityDuration, DifficultyAdjustmentWindowSize, PruningMultiplier, MaxBlockMass,
//     MaxBlockParents, MergeDepth. Do not copy the previous version's values by reflex; each one is
//     a decision. TestPerVersionTablesCoverEveryActivatedVersion fails if any table is forgotten.
//  3. Replace the relevant constant below with that new version number.
//
// Steps 1 and 2 without step 3 are safe: the rule simply stays off. Step 3 without steps 1 and 2 is
// also safe, because no block can reach an unscheduled version. What is not safe is picking an
// activation version that a network can already reach, which is what TestGatesAreInertOnEveryNetwork
// exists to prevent.
package hardforks

import (
	"math"
)

// unscheduledActivation marks a rule whose activation version has not been chosen.
//
// math.MaxUint16 is unreachable by construction, not merely large:
// constants.BlockVersionForDAAScore returns at most len(POWScores)+1, and the longest table on any
// network has single-digit length. So a gate left at this value answers false for every block that
// can exist, on every network, including a node whose process-global version has been forced
// arbitrarily high.
//
// It is deliberately not 0 and not a plausible-looking number like 11. A gate accidentally left at
// a small value would silently activate at the next hard fork; one left at this value cannot
// activate at all, and the test suite says so out loud.
const unscheduledActivation uint16 = math.MaxUint16

// The activation version of each dormant rule. Each is independent: scheduling one says nothing
// about the others, and they are expected to be activated together only because a single hard fork
// is cheaper than four.
//
// These are vars rather than consts solely so that SetForTest can drive a rule's activated
// behaviour, which is the only way to test a rule that is by definition unreachable. Nothing in
// production ever assigns to them - the CI grep in build_and_test.sh enforces that - and the
// precedent is constants.ForceSetBlockVersion, which exists for the same reason.
var (
	// StrictUTXOCommitmentVersion activates HTN-002/HTN-004: from this block version,
	// verifyAndBuildUTXO stops swallowing RuleErrors from the UTXO commitment, accepted-ID merkle
	// root, coinbase and body-vs-past-UTXO checks on a node running an inherited-offset baseline.
	//
	// Today those four failures are downgraded to a log line whenever this node's own pruning-point
	// UTXO set does not hash to its commitment, which is the state essentially every mainnet node
	// is in. Enabling this before a coordinated rebaseline would disqualify the live chain.
	StrictUTXOCommitmentVersion = unscheduledActivation

	// RefuseMismatchedImportVersion activates HTN-005: from this block version, an imported
	// pruning-point UTXO set whose MuHash disagrees with the commitment is refused rather than
	// accepted-and-repaired, and this node refuses to serve such a set onward.
	//
	// This is gated separately from the operator flag EnableSanityCheckPruningUTXOSet
	// (--enable-sanity-check-pruning-utxo), which stays exactly as it was. That flag is off by
	// default and must remain so: no node currently serves a commitment-matching pruning-point set,
	// so refusing today means being unable to sync at all.
	RefuseMismatchedImportVersion = unscheduledActivation

	// ValidateHeaderBitsVersion activates HTN-007: from this block version, a header's bits must
	// equal the difficulty this node computes for it.
	//
	// Bits are currently never checked. ValidatePruningPointViolationAndProofOfWorkAndDifficulty
	// calls StageDAAData, which stages the window and discards the required difficulty, under a
	// comment claiming the header's difficulty is validated. Proof of work is still checked against
	// the header's own bits, so a miner cannot claim easy work and skip it - but it can claim a
	// difficulty the rest of the network did not agree on.
	ValidateHeaderBitsVersion = unscheduledActivation

	// ValidateIBDPruningListVersion activates HTN-006: from this block version, an imported pruning
	// point is checked with IsValidPruningPoint, and the pruning point list is checked to form a
	// valid chain to genesis with ArePruningPointsInValidChain.
	//
	// Both checks are commented out today with the note that HTN pruning points are "messed up".
	// The measured blue-score mismatch rate makes it likely that turning them on now would reject
	// the majority of the existing chain.
	ValidateIBDPruningListVersion = unscheduledActivation
)

// IsScheduled reports whether activationVersion names a real block version rather than the
// unscheduled placeholder.
func IsScheduled(activationVersion uint16) bool {
	return activationVersion != unscheduledActivation
}

// Active reports whether the rule gated at activationVersion applies to a block of blockVersion.
//
// blockVersion must be a version this node derived itself from a DAA score it computed - via
// constants.BlockVersionForDAAScore, blockversion.OfSelectedParent, or an equivalent - and never
// the version field of a peer-supplied header. Every rule gated here adds strictness, so keying one
// on an attacker-chosen field would let any miner opt out of it by claiming an older version.
func Active(activationVersion, blockVersion uint16) bool {
	return IsScheduled(activationVersion) && blockVersion >= activationVersion
}

// SetForTest sets gate to activationVersion and returns a function that restores its previous
// value. It exists because every rule in this package is, by design, unreachable - so without it
// the activated side of each rule could never be tested, and a dormant rule that has never been
// executed is not a rule that has been written, it is a rule that has been drafted.
//
// Test-only, and deliberately awkward to reach for: it takes a pointer so that each call names
// exactly one gate at its definition site, rather than silently activating all four.
//
// These gates are process-global, so a test using this must not run in parallel with another that
// reads the same gate.
//
//	defer hardforks.SetForTest(&hardforks.ValidateHeaderBitsVersion, 2)()
func SetForTest(gate *uint16, activationVersion uint16) func() {
	previous := *gate
	*gate = activationVersion
	return func() { *gate = previous }
}
