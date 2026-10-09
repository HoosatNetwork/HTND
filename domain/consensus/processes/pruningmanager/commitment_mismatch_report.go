package pruningmanager

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/constants"
)

// [PP-COMMITMENT] report: what this node knew when the pruning point UTXO set it is about to serve
// failed its header commitment.
//
// A failing commitment on its own is two hashes, and MuHash cannot be decomposed: the hashes say the
// sets differ and nothing about how. What the node can do is narrow down WHERE the difference
// entered, using records it already has:
//
//   - The previous pruning point's per-block multiset against its own header. If it already
//     disagreed, the new point inherits that offset and its own mismatch is a symptom, not a cause.
//   - The new pruning point's per-block multiset against its header and against the served bucket.
//     Per-block resolution and the bucket update are separate code paths, so which of them agrees
//     with the header says which one to blame.
//   - Every selected-chain block between the two pruning points, stored multiset against header
//     commitment. The block where that flips from matching to not matching is where the difference
//     entered on this node, and that block's acceptance data is printed so it can be compared with a
//     node that resolved it differently.
//   - The two ways of deriving the pruning-point diff (acceptance-data replay and the UTXO-diff
//     chain walk) compared entry by entry. They should be identical; any outpoint they disagree on
//     is a concrete coin to trace.
//
// All of it is diagnostic. Nothing here changes state or the outcome of the advancement.

// Bounds on how much one report prints. The walk visits every chain block between the two pruning
// points; only these many of each finding are spelled out, and the totals are always given.
const (
	maxCommitmentReportTransitions      = 20
	maxCommitmentReportDiffExamples     = 20
	maxCommitmentReportRejectedExamples = 20
)

// utxoSetStats is what one pass over a UTXO set records besides its multiset: enough to tell
// "a handful of coins differ" from "a whole class of coins is missing" without a second pass.
type utxoSetStats struct {
	entries         int
	totalSompi      uint64
	coinbaseEntries int
	coinbaseSompi   uint64
	minDAAScore     uint64
	maxDAAScore     uint64
}

func (s *utxoSetStats) add(entry externalapi.UTXOEntry) {
	if s.entries == 0 || entry.BlockDAAScore() < s.minDAAScore {
		s.minDAAScore = entry.BlockDAAScore()
	}
	if entry.BlockDAAScore() > s.maxDAAScore {
		s.maxDAAScore = entry.BlockDAAScore()
	}
	s.entries++
	s.totalSompi += entry.Amount()
	if entry.IsCoinbase() {
		s.coinbaseEntries++
		s.coinbaseSompi += entry.Amount()
	}
}

func (s *utxoSetStats) String() string {
	return fmt.Sprintf("%d entries totalling %d sompi (%d coinbase entries, %d sompi), entry DAA scores %d..%d",
		s.entries, s.totalSompi, s.coinbaseEntries, s.coinbaseSompi, s.minDAAScore, s.maxDAAScore)
}

// reportPruningPointCommitmentMismatch logs everything above for pruningPoint. bucketHash and
// bucketStats describe the served set as validateUTXOSetFitsCommitment just hashed it.
//
// The diff that advanced the bucket is not passed in, because holding it kept every entry of it alive
// through the full-set validation pass. The caller passes its sizes (diffToAdd, diffToRemove) and
// rederiveDiff, which rebuilds it on demand. Both are zero/nil when no diff is at hand (the startup
// check), and rederiveDiff may return nil if the diff can no longer be derived.
func (pm *pruningManager) reportPruningPointCommitmentMismatch(stagingArea *model.StagingArea,
	pruningPoint *externalapi.DomainHash, bucketHash *externalapi.DomainHash, bucketStats *utxoSetStats,
	diffMethod string, diffToAdd, diffToRemove int, rederiveDiff func() externalapi.UTXODiff,
) {
	start := time.Now()
	defer func() {
		log.Infof("[PP-COMMITMENT] report for pruning point %s took %s", pruningPoint, time.Since(start))
	}()

	header, err := pm.blockHeaderStore.BlockHeader(pm.databaseContext, stagingArea, pruningPoint)
	if err != nil {
		log.Warnf("[PP-COMMITMENT] pruning point %s: could not fetch its header: %s", pruningPoint, err)
		return
	}
	expected := header.UTXOCommitment()

	pruningPointIndex, err := pm.pruningStore.CurrentPruningPointIndex(pm.databaseContext, stagingArea)
	if err != nil {
		log.Warnf("[PP-COMMITMENT] pruning point %s: could not fetch the pruning point index: %s", pruningPoint, err)
		return
	}

	log.Warnf("[PP-COMMITMENT] pruning point %s (index %d, DAA score %d, blue score %d, block version %d, "+
		"header version %d): header commits to %s, served bucket hashes to %s over %s",
		pruningPoint, pruningPointIndex, header.DAAScore(), header.BlueScore(),
		constants.BlockVersionForDAAScore(pm.powScores, header.DAAScore()), header.Version(),
		expected, bucketHash, bucketStats)

	currentPerBlock := pm.perBlockMultisetHash(stagingArea, pruningPoint)
	log.Warnf("[PP-COMMITMENT] pruning point %s: per-block multiset %s (matches header: %t, matches bucket: %t)",
		pruningPoint, currentPerBlock, currentPerBlock != nil && currentPerBlock.Equal(expected),
		currentPerBlock != nil && currentPerBlock.Equal(bucketHash))

	if rederiveDiff != nil {
		log.Warnf("[PP-COMMITMENT] pruning point %s: the bucket was advanced with the %s diff (%d to add, %d to remove)",
			pruningPoint, diffMethod, diffToAdd, diffToRemove)
	}

	if pruningPointIndex == 0 {
		log.Warnf("[PP-COMMITMENT] pruning point %s is the first recorded one; there is no previous pruning point "+
			"to localize against", pruningPoint)
		return
	}
	previousPruningPoint, err := pm.pruningStore.PruningPointByIndex(pm.databaseContext, stagingArea, pruningPointIndex-1)
	if err != nil {
		log.Warnf("[PP-COMMITMENT] pruning point %s: could not fetch the previous pruning point: %s", pruningPoint, err)
		return
	}
	previousClean := false
	if previousHeader, err := pm.blockHeaderStore.BlockHeader(pm.databaseContext, stagingArea, previousPruningPoint); err != nil {
		log.Warnf("[PP-COMMITMENT] previous pruning point %s: could not fetch its header: %s", previousPruningPoint, err)
	} else {
		previousPerBlock := pm.perBlockMultisetHash(stagingArea, previousPruningPoint)
		previousClean = previousPerBlock != nil && previousPerBlock.Equal(previousHeader.UTXOCommitment())
		log.Warnf("[PP-COMMITMENT] previous pruning point %s (DAA score %d): header commits to %s, per-block "+
			"multiset %s (matches header: %t)", previousPruningPoint, previousHeader.DAAScore(),
			previousHeader.UTXOCommitment(), previousPerBlock, previousClean)
	}

	log.Warnf("[PP-COMMITMENT] pruning point %s: %s", pruningPoint,
		classifyPruningPointMismatch(previousClean, currentPerBlock, expected, bucketHash))

	pm.walkChainCommitments(stagingArea, previousPruningPoint, pruningPoint)

	// Last, and only now: rebuilding the diff is the expensive step, and nothing above needed it. With no
	// rederiveDiff (startup check) or a failed re-derivation, usedDiff stays nil and compareDiffDerivations
	// derives the acceptance-data diff itself.
	var usedDiff externalapi.UTXODiff
	if rederiveDiff != nil {
		usedDiff = rederiveDiff()
	}
	pm.compareDiffDerivations(stagingArea, pruningPoint, diffMethod, usedDiff)
}

// classifyPruningPointMismatch turns the three comparisons into which code path to look at.
func classifyPruningPointMismatch(previousClean bool, currentPerBlock, expected, bucketHash *externalapi.DomainHash) string {
	perBlockClean := currentPerBlock != nil && currentPerBlock.Equal(expected)
	perBlockAgreesWithBucket := currentPerBlock != nil && currentPerBlock.Equal(bucketHash)
	switch {
	case perBlockClean:
		return "VERDICT bucket-derivation: per-block resolution reproduces the header but the served bucket does " +
			"not, so the defect is in how the bucket was advanced (the diff applied to it, or the bucket it was " +
			"applied to), not in block resolution. See the diff comparison below."
	case currentPerBlock == nil:
		return "VERDICT unknown: the per-block multiset is unavailable, so the bucket cannot be told apart from " +
			"block resolution."
	case !previousClean && perBlockAgreesWithBucket:
		return "VERDICT inherited: the previous pruning point already disagreed with its own header, and the bucket " +
			"agrees with this node's per-block multiset, so this advancement carried the existing offset forward " +
			"correctly. The cause is at or before the previous pruning point; the chain walk below shows whether " +
			"anything new entered in between."
	case previousClean && perBlockAgreesWithBucket:
		return "VERDICT entered-this-interval: the previous pruning point matched its header and this one does not, " +
			"and bucket and per-block resolution agree with each other. Something in block resolution between the " +
			"two pruning points diverged; the chain walk below names the block."
	case previousClean:
		return "VERDICT entered-this-interval-and-bucket-drift: the previous pruning point was clean, and now the " +
			"header, the per-block multiset and the bucket all disagree. Block resolution diverged in this interval " +
			"AND the bucket update disagrees with it."
	default:
		return "VERDICT inherited-and-bucket-drift: the previous pruning point was already offset, and the bucket " +
			"now also disagrees with this node's per-block multiset, so the bucket update added a difference of its " +
			"own on top of the inherited one."
	}
}

func (pm *pruningManager) perBlockMultisetHash(stagingArea *model.StagingArea,
	blockHash *externalapi.DomainHash,
) *externalapi.DomainHash {
	ms, err := pm.multiSetStore.Get(pm.databaseContext, stagingArea, blockHash)
	if err != nil {
		return nil
	}
	return ms.Hash()
}

// chainCommitmentState is one chain block's standing: whether its stored multiset reproduces its
// own header commitment, or whether that could not be determined.
type chainCommitmentState int

const (
	chainCommitmentUnknown chainCommitmentState = iota
	chainCommitmentMatches
	chainCommitmentMismatches
)

func (s chainCommitmentState) String() string {
	switch s {
	case chainCommitmentMatches:
		return "matches"
	case chainCommitmentMismatches:
		return "mismatches"
	default:
		return "unknown"
	}
}

// walkChainCommitments compares every selected-chain block after previousPruningPoint up to and
// including pruningPoint against its own header, and reports each point where that changes. A block
// where the chain goes from matching to mismatching is where this node's UTXO view first differed
// from the miner's; its acceptance data is printed in full.
func (pm *pruningManager) walkChainCommitments(stagingArea *model.StagingArea,
	previousPruningPoint, pruningPoint *externalapi.DomainHash,
) {
	iterator, err := pm.dagTraversalManager.SelectedChildIterator(stagingArea, pruningPoint, previousPruningPoint, false)
	if err != nil {
		log.Warnf("[PP-COMMITMENT] chain walk %s..%s: could not iterate the selected chain: %s",
			previousPruningPoint, pruningPoint, err)
		return
	}
	defer iterator.Close()

	previousState := chainCommitmentUnknown
	if header, err := pm.blockHeaderStore.BlockHeader(pm.databaseContext, stagingArea, previousPruningPoint); err == nil {
		previousState = pm.chainCommitmentStateOf(stagingArea, previousPruningPoint, header)
	}
	previousBlock := previousPruningPoint

	counts := map[chainCommitmentState]int{}
	transitions := 0
	firstEntry := (*externalapi.DomainHash)(nil)
	for ok := iterator.First(); ok; ok = iterator.Next() {
		blockHash, err := iterator.Get()
		if err != nil {
			log.Warnf("[PP-COMMITMENT] chain walk %s..%s: iterator failed: %s", previousPruningPoint, pruningPoint, err)
			return
		}
		header, err := pm.blockHeaderStore.BlockHeader(pm.databaseContext, stagingArea, blockHash)
		if err != nil {
			counts[chainCommitmentUnknown]++
			continue
		}
		state := pm.chainCommitmentStateOf(stagingArea, blockHash, header)
		counts[state]++
		if state != previousState && state != chainCommitmentUnknown {
			transitions++
			if transitions <= maxCommitmentReportTransitions {
				status, _ := pm.blockStatusStore.Get(pm.databaseContext, stagingArea, blockHash)
				log.Warnf("[PP-COMMITMENT] chain walk: block %s (DAA score %d, blue score %d, block version %d, "+
					"status %s) %s its header commitment %s; its selected parent %s %s. Stored multiset: %s",
					blockHash, header.DAAScore(), header.BlueScore(),
					constants.BlockVersionForDAAScore(pm.powScores, header.DAAScore()), status, state,
					header.UTXOCommitment(), previousBlock, previousState, pm.perBlockMultisetHash(stagingArea, blockHash))
			}
			if state == chainCommitmentMismatches && previousState == chainCommitmentMatches && firstEntry == nil {
				firstEntry = blockHash
				pm.logAcceptanceDataSummary(stagingArea, blockHash)
			}
		}
		if state != chainCommitmentUnknown {
			previousState = state
		}
		previousBlock = blockHash
	}

	log.Warnf("[PP-COMMITMENT] chain walk %s..%s: %d chain blocks match their header, %d mismatch, %d could "+
		"not be checked (no stored multiset or header); %d transitions (first %d logged)",
		previousPruningPoint, pruningPoint, counts[chainCommitmentMatches], counts[chainCommitmentMismatches],
		counts[chainCommitmentUnknown], transitions, min(transitions, maxCommitmentReportTransitions))
	if firstEntry == nil && counts[chainCommitmentMismatches] > 0 {
		log.Warnf("[PP-COMMITMENT] chain walk %s..%s: every checkable chain block already mismatches, starting "+
			"from the previous pruning point - no new divergence entered in this interval on this node",
			previousPruningPoint, pruningPoint)
	}
}

func (pm *pruningManager) chainCommitmentStateOf(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash,
	header externalapi.BlockHeader,
) chainCommitmentState {
	stored := pm.perBlockMultisetHash(stagingArea, blockHash)
	if stored == nil {
		return chainCommitmentUnknown
	}
	if stored.Equal(header.UTXOCommitment()) {
		return chainCommitmentMatches
	}
	return chainCommitmentMismatches
}

// logAcceptanceDataSummary prints what blockHash merged and what it declined. The declined
// transactions are the first place to look: a transaction accepted by the miner and rejected here
// (or the reverse) is exactly a commitment difference, and the [TX-VERDICT] debug lines from
// consensusstatemanager give the per-input reason for each one.
func (pm *pruningManager) logAcceptanceDataSummary(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash) {
	acceptanceData, err := pm.acceptanceDataStore.Get(pm.databaseContext, stagingArea, blockHash)
	if err != nil {
		log.Warnf("[PP-COMMITMENT] divergence entry %s: could not fetch its acceptance data: %s", blockHash, err)
		return
	}
	log.Warnf("[PP-COMMITMENT] divergence entry %s: %s", blockHash, describeAcceptanceData(acceptanceData))
}

func describeAcceptanceData(acceptanceData externalapi.AcceptanceData) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "merge set of %d blocks", len(acceptanceData))
	var rejected []string
	totalRejected := 0
	for i, blockAcceptanceData := range acceptanceData {
		if blockAcceptanceData == nil {
			continue
		}
		accepted := 0
		var acceptedFees uint64
		for _, transactionAcceptanceData := range blockAcceptanceData.TransactionAcceptanceData {
			if transactionAcceptanceData == nil {
				continue
			}
			if transactionAcceptanceData.IsAccepted {
				accepted++
				acceptedFees += transactionAcceptanceData.Fee
				continue
			}
			totalRejected++
			if len(rejected) < maxCommitmentReportRejectedExamples && transactionAcceptanceData.Transaction != nil {
				rejected = append(rejected, fmt.Sprintf("%s (from %s, %d inputs, %d outputs)",
					consensushashing.TransactionID(transactionAcceptanceData.Transaction), blockAcceptanceData.BlockHash,
					len(transactionAcceptanceData.Transaction.Inputs), len(transactionAcceptanceData.Transaction.Outputs)))
			}
		}
		role := "merged"
		if i == 0 {
			role = "selected parent"
		}
		fmt.Fprintf(&builder, "; %s %s: %d/%d transactions accepted, fees %d", role, blockAcceptanceData.BlockHash,
			accepted, len(blockAcceptanceData.TransactionAcceptanceData), acceptedFees)
	}
	fmt.Fprintf(&builder, "; %d transactions not accepted", totalRejected)
	if len(rejected) > 0 {
		fmt.Fprintf(&builder, " (first %d: %s)", len(rejected), strings.Join(rejected, ", "))
	}
	return builder.String()
}

// compareDiffDerivations derives the pruning-point diff the other way and reports every outpoint on
// which the two derivations disagree. Both describe the same transition from the same records, so
// any disagreement is a coin one of them got wrong.
func (pm *pruningManager) compareDiffDerivations(stagingArea *model.StagingArea, pruningPoint *externalapi.DomainHash,
	usedMethod string, usedDiff externalapi.UTXODiff,
) {
	if usedDiff == nil {
		// Reported from outside an advancement (the startup check), where the diff that built the
		// bucket is not at hand: derive the acceptance-data one and compare the other against it.
		var err error
		usedMethod = "acceptance-data"
		usedDiff, err = pm.calculateDiffBetweenPreviousAndCurrentPruningPointsUsingAcceptanceData(stagingArea, pruningPoint)
		if err != nil {
			log.Warnf("[PP-COMMITMENT] pruning point %s: could not derive the acceptance-data diff: %s", pruningPoint, err)
			return
		}
	}
	otherMethod := "diff-chain-walk"
	otherDiff, err := pm.calculateDiffBetweenPreviousAndCurrentPruningPoints(stagingArea, pruningPoint)
	if usedMethod == "diff-chain-walk" {
		otherMethod = "acceptance-data"
		otherDiff, err = pm.calculateDiffBetweenPreviousAndCurrentPruningPointsUsingAcceptanceData(stagingArea, pruningPoint)
	}
	if err != nil {
		log.Warnf("[PP-COMMITMENT] pruning point %s: could not derive the %s diff to compare against the %s one: %s",
			pruningPoint, otherMethod, usedMethod, err)
		return
	}
	log.Warnf("[PP-COMMITMENT] pruning point %s: %s", pruningPoint,
		describeUTXODiffDisagreement(usedMethod, usedDiff, otherMethod, otherDiff, maxCommitmentReportDiffExamples))
}

// describeUTXODiffDisagreement lists the outpoints on which two diffs of the same transition
// disagree: present in one side's toAdd or toRemove and not the other's, or present in both with a
// different entry. An entry that differs only in DAA score is called out, because the DAA score an
// accepted UTXO is stamped with is part of the commitment preimage and has been the cause before.
func describeUTXODiffDisagreement(nameA string, diffA externalapi.UTXODiff, nameB string, diffB externalapi.UTXODiff,
	maxExamples int,
) string {
	var examples []string
	counts := map[string]int{}
	note := func(kind string, outpoint *externalapi.DomainOutpoint, detail string) {
		counts[kind]++
		if len(examples) < maxExamples {
			examples = append(examples, fmt.Sprintf("%s %s %s", kind, outpoint, detail))
		}
	}
	compare := func(side string, a, b externalapi.UTXOCollection, onlyAKind, onlyBKind string) {
		iterator := a.Iterator()
		for ok := iterator.First(); ok; ok = iterator.Next() {
			outpoint, entry, err := iterator.Get()
			if err != nil {
				continue
			}
			other, found := b.Get(outpoint)
			if !found {
				note(onlyAKind, outpoint, describeEntry(entry))
				continue
			}
			if !entry.Equal(other) {
				note(side+"-entry-differs", outpoint, fmt.Sprintf("%s: %s vs %s: %s (%s)",
					nameA, describeEntry(entry), nameB, describeEntry(other), entryDifference(entry, other)))
			}
		}
		iterator.Close()
		iterator = b.Iterator()
		for ok := iterator.First(); ok; ok = iterator.Next() {
			outpoint, entry, err := iterator.Get()
			if err != nil {
				continue
			}
			if !a.Contains(outpoint) {
				note(onlyBKind, outpoint, describeEntry(entry))
			}
		}
		iterator.Close()
	}
	compare("toAdd", diffA.ToAdd(), diffB.ToAdd(), "toAdd-only-in-"+nameA, "toAdd-only-in-"+nameB)
	compare("toRemove", diffA.ToRemove(), diffB.ToRemove(), "toRemove-only-in-"+nameA, "toRemove-only-in-"+nameB)

	total := 0
	for _, count := range counts {
		total += count
	}
	header := fmt.Sprintf("diff derivations %s (%d to add, %d to remove) and %s (%d to add, %d to remove)",
		nameA, diffA.ToAdd().Len(), diffA.ToRemove().Len(), nameB, diffB.ToAdd().Len(), diffB.ToRemove().Len())
	if total == 0 {
		return header + " AGREE on every outpoint, so the served set's difference from the header is not in how " +
			"the diff was derived: it is in the bucket the diff was applied to, or in the records both read"
	}
	kinds := make([]string, 0, len(counts))
	for _, kind := range []string{
		"toAdd-only-in-" + nameA, "toAdd-only-in-" + nameB, "toAdd-entry-differs",
		"toRemove-only-in-" + nameA, "toRemove-only-in-" + nameB, "toRemove-entry-differs",
	} {
		if counts[kind] > 0 {
			kinds = append(kinds, fmt.Sprintf("%s=%d", kind, counts[kind]))
		}
	}
	return fmt.Sprintf("%s DISAGREE on %d outpoints (%s); first %d: %s", header, total, strings.Join(kinds, ", "),
		len(examples), strings.Join(examples, "; "))
}

func describeEntry(entry externalapi.UTXOEntry) string {
	if entry == nil {
		return "<nil>"
	}
	scriptLength := 0
	if entry.ScriptPublicKey() != nil {
		scriptLength = len(entry.ScriptPublicKey().Script)
	}
	return fmt.Sprintf("[amount %d, DAA score %d, coinbase %t, script %d bytes]", entry.Amount(),
		entry.BlockDAAScore(), entry.IsCoinbase(), scriptLength)
}

func entryDifference(a, b externalapi.UTXOEntry) string {
	var differences []string
	if a.Amount() != b.Amount() {
		differences = append(differences, "amount")
	}
	if a.BlockDAAScore() != b.BlockDAAScore() {
		differences = append(differences, fmt.Sprintf("DAA score by %d", int64(a.BlockDAAScore())-int64(b.BlockDAAScore())))
	}
	if a.IsCoinbase() != b.IsCoinbase() {
		differences = append(differences, "coinbase flag")
	}
	scriptA, scriptB := a.ScriptPublicKey(), b.ScriptPublicKey()
	if (scriptA == nil) != (scriptB == nil) ||
		(scriptA != nil && (scriptA.Version != scriptB.Version || !bytes.Equal(scriptA.Script, scriptB.Script))) {
		differences = append(differences, "script")
	}
	if len(differences) == 0 {
		return "no field differs"
	}
	return "differs in " + strings.Join(differences, ", ")
}
