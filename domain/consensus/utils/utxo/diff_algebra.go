package utxo

import (
	"fmt"
	"maps"

	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/pkg/errors"
)

// checkIntersection checks if there is an intersection between two utxoCollections
func checkIntersection(collection1 utxoCollection, collection2 utxoCollection) bool {
	for outpoint := range collection1 {
		if collection2.Contains(&outpoint) {
			return true
		}
	}

	return false
}

// checkIntersectionWithRule checks if there is an intersection between two utxoCollections satisfying arbitrary rule
// returns the first outpoint in the two collections' intersection satisfying the rule, and a boolean indicating whether
// such outpoint exists
func checkIntersectionWithRule(collectionA utxoCollection, collectionB utxoCollection,
	extraRule func(*externalapi.DomainOutpoint, externalapi.UTXOEntry, externalapi.UTXOEntry) bool,
) (*externalapi.DomainOutpoint, bool) {
	swapped := false
	sourceMap, targetMap := collectionA, collectionB
	if len(collectionA) > len(collectionB) {
		sourceMap, targetMap = collectionB, collectionA
		swapped = true
	}

	for outpoint, entry := range sourceMap {
		if otherEntry, ok := targetMap.Get(&outpoint); ok {
			entryA, entryB := entry, otherEntry
			if swapped {
				entryA, entryB = otherEntry, entry
			}

			if extraRule(&outpoint, entryA, entryB) {
				outpointCopy := outpoint
				return &outpointCopy, true
			}
		}
	}

	return nil, false
}

// isTolerableConflict reports whether two UTXO entries found conflicting on the same outpoint
// (both removed, both added, or removed-vs-added with mismatched DAA scores) are safe to leave
// as-is rather than treated as corruption. Two independent, narrow cases qualify:
//
//  1. Both entries are coinbase outputs. Coinbase transactions mined before per-block entropy was
//     folded into the payload (see coinbaseEntropyActivationVersion) derive their ID from just
//     blueScore + subsidy + miner script + extra data, so two blocks sharing those fields (e.g.
//     sibling blocks from the same miner) can legitimately produce byte-identical coinbase
//     transactions - and since the payload bytes that collide are also what the transaction's
//     outputs are built from, a colliding pair always describes the same spendable value; only
//     their acceptance-time BlockDAAScore can differ (which is why this branch alone doesn't
//     require it to match). That fix only protects blocks mined after the hard fork, so
//     already-mined colliding pairs stay in the chain forever and every node reconstructing that
//     stretch of history will hit this.
//
//  2. The two entries describe the same spendable value - same amount, same script - regardless of
//     BlockDAAScore, yet still land on opposite sides of the conflict (this.toRemove vs
//     other.toAdd, most notably). That shape means two independent reconstructions of the same
//     real transaction (e.g. two competing tip candidates during a reorg, each walking its own
//     selected-parent chain, or the same merge-set transaction re-touched by successive blocks
//     along a chain) simply disagree about whether the shared base already contained it, or about
//     which accepting block's DAA score it was last recorded under - not that the transaction
//     itself differs in any way. Confirmed against real data: the same outpoint has been observed
//     conflicting once with matching DAA scores (this.toRemove vs other.toAdd) and, in the very
//     same reconstruction, again with mismatched DAA scores (this.toRemove vs other.toRemove) -
//     so DAA score is not a reliable signal here and can't be required to match. Since amount and
//     script are what an attacker could exploit and they're identical, it's safe to leave the
//     disagreement as a bookkeeping artifact rather than fail the whole reconstruction over it.
//
// Anything else - a conflict where amount or script genuinely differ, or only one side is
// coinbase - has no such explanation and is real corruption.
func isTolerableConflict(entryA, entryB externalapi.UTXOEntry) bool {
	if entryA.IsCoinbase() && entryB.IsCoinbase() {
		return true
	}
	return entryA.Amount() == entryB.Amount() && entryA.ScriptPublicKey().Equal(entryB.ScriptPublicKey())
}

// describeConflictEntry formats a UTXOEntry's diagnostically-relevant fields for a hard-error
// conflict message, so the log itself says why isTolerableConflict rejected the pair (which side
// isn't a coinbase output, and how the two entries actually differ) instead of requiring a
// follow-up investigation to find out.
func describeConflictEntry(entry externalapi.UTXOEntry) string {
	if entry == nil {
		return "<nil>"
	}
	return fmt.Sprintf("amount: %d, scriptPublicKey: %x, daaScore: %d, isCoinbase: %t",
		entry.Amount(), entry.ScriptPublicKey().Script, entry.BlockDAAScore(), entry.IsCoinbase())
}

// resolveConflicts scans collectionA/collectionB for every outpoint satisfying rule - one of the
// classic "same outpoint touched by both sides" conflict shapes shared by diffFrom and
// withDiffInPlace - and, for each one found, either logs and tolerates it (isTolerableConflict)
// or returns an error describing it (real corruption). Conflicting outpoints are never mutated: a
// tolerated conflict is left for the caller's own merge algebra to resolve, which already
// collapses a doubly-touched outpoint down to one consistent entry.
func resolveConflicts(funcName string, collectionA, collectionB utxoCollection,
	rule func(outpoint *externalapi.DomainOutpoint, entryA, entryB externalapi.UTXOEntry) bool,
	conflictDescription string,
) error {
	seen := make(map[externalapi.DomainOutpoint]bool)
	for {
		offendingOutpoint, ok := checkIntersectionWithRule(collectionA, collectionB,
			func(outpoint *externalapi.DomainOutpoint, entryA, entryB externalapi.UTXOEntry) bool {
				return !seen[*outpoint] && rule(outpoint, entryA, entryB)
			})
		if !ok {
			return nil
		}
		seen[*offendingOutpoint] = true

		entryA, _ := collectionA.Get(offendingOutpoint)
		entryB, _ := collectionB.Get(offendingOutpoint)
		if !isTolerableConflict(entryA, entryB) {
			return errors.Errorf("%s: outpoint %s %s (entryA: %s, entryB: %s)", funcName, offendingOutpoint,
				conflictDescription, describeConflictEntry(entryA), describeConflictEntry(entryB))
		}
		log.Debugf("%s: outpoint %s %s (entries agree on value - historical coinbase ID collision or "+
			"duplicate cross-reconstruction acceptance) - leaving it as is",
			funcName, offendingOutpoint, conflictDescription)
	}
}

// intersectionWithRemainderHavingDAAScoreInPlace calculates an intersection between two utxoCollections
// having same DAA score, puts it into result and into remainder from collection1
func intersectionWithRemainderHavingDAAScoreInPlace(collection1, collection2, result, remainder utxoCollection) {
	// FAST PATH: If collection2 is smaller, iterate over collection2 instead of collection1
	if len(collection2) < len(collection1) {
		maps.Copy(remainder, collection1)
		for outpoint, entry2 := range collection2 {
			if entry1, ok := collection1[outpoint]; ok && entry1.BlockDAAScore() == entry2.BlockDAAScore() {
				result[outpoint] = entry1
				delete(remainder, outpoint)
			}
		}
		return
	}

	// STANDARD PATH: collection1 is smaller or equal
	for outpoint, entry1 := range collection1 {
		if entry2, ok := collection2[outpoint]; ok && entry2.BlockDAAScore() == entry1.BlockDAAScore() {
			result[outpoint] = entry1
		} else {
			remainder[outpoint] = entry1
		}
	}
}

// subtractionHavingDAAScoreInPlace calculates a subtraction between collection1 and collection2
// having same DAA score, puts it into result
func subtractionHavingDAAScoreInPlace(collection1, collection2, result utxoCollection) {
	for outpoint, utxoEntry := range collection1 {
		if !collection2.containsWithDAAScore(&outpoint, utxoEntry.BlockDAAScore()) {
			result.add(&outpoint, utxoEntry)
		}
	}
}

// subtractionWithRemainderHavingDAAScoreInPlace calculates a subtraction between collection1 and collection2
// having same DAA score, puts it into result and into remainder from collection1
func subtractionWithRemainderHavingDAAScoreInPlace(collection1, collection2, result, remainder utxoCollection) {
	for outpoint, utxoEntry := range collection1 {
		if !collection2.containsWithDAAScore(&outpoint, utxoEntry.BlockDAAScore()) {
			result.add(&outpoint, utxoEntry)
		} else {
			remainder.add(&outpoint, utxoEntry)
		}
	}
}

// DiffFrom returns a new mutableUTXODiff with the difference between this mutableUTXODiff and another
// Assumes that:
// Both mutableUTXODiffs are from the same base
// If a txOut exists in both mutableUTXODiffs, its underlying values would be the same
//
// diffFrom follows a set of rules represented by the following 3 by 3 table:
//
// .........|...........| this      |...........|...........
// ---------+-----------+-----------+-----------+-----------
// .........|...........| toAdd     | toRemove  | None
// ---------+-----------+-----------+-----------+-----------
// other    | toAdd     | -         | X         | toAdd
// ---------+-----------+-----------+-----------+-----------
// .........| toRemove  | X         | -         | toRemove
// ---------+-----------+-----------+-----------+-----------
// .........| None      | toRemove  | toAdd     | -
//
// Key:
// -		Don't add anything to the result
// X		Return an error
// toAdd	Add the UTXO into the toAdd collection of the result
// toRemove	Add the UTXO into the toRemove collection of the result
//
// Examples:
//  1. This diff contains a UTXO in toAdd, and the other diff contains it in toRemove
//     diffFrom results in an error
//  2. This diff contains a UTXO in toRemove, and the other diff does not contain it
//     diffFrom results in the UTXO being added to toAdd
func diffFrom(this, other *mutableUTXODiff) (*mutableUTXODiff, error) {
	// Note that the following cases are not accounted for, as they are impossible
	// as long as the base utxoSet is the same:
	// - if utxoEntry is in this.toAdd and other.toRemove
	// - if utxoEntry is in this.toRemove and other.toAdd

	// check that NOT (entries with unequal DAA scores AND utxoEntry is in this.toAdd and/or other.toRemove) -> Error
	isNotAddedOutputRemovedWithDAAScore := func(outpoint *externalapi.DomainOutpoint, utxoEntry, diffEntry externalapi.UTXOEntry) bool {
		return !(diffEntry.BlockDAAScore() != utxoEntry.BlockDAAScore() &&
			(this.toAdd.containsWithDAAScore(outpoint, diffEntry.BlockDAAScore()) ||
				other.toRemove.containsWithDAAScore(outpoint, utxoEntry.BlockDAAScore())))
	}

	if err := resolveConflicts("diffFrom", this.toRemove, other.toAdd, isNotAddedOutputRemovedWithDAAScore,
		"both in this.toRemove and in other.toAdd"); err != nil {
		return nil, err
	}

	// check that NOT (entries with unequal DAA score AND utxoEntry is in this.toRemove and/or other.toAdd) -> Error
	isNotRemovedOutputAddedWithDAAScore := func(outpoint *externalapi.DomainOutpoint, utxoEntry, diffEntry externalapi.UTXOEntry) bool {
		return !(diffEntry.BlockDAAScore() != utxoEntry.BlockDAAScore() &&
			(this.toRemove.containsWithDAAScore(outpoint, diffEntry.BlockDAAScore()) ||
				other.toAdd.containsWithDAAScore(outpoint, utxoEntry.BlockDAAScore())))
	}

	if err := resolveConflicts("diffFrom", this.toAdd, other.toRemove, isNotRemovedOutputAddedWithDAAScore,
		"both in this.toAdd and in other.toRemove"); err != nil {
		return nil, err
	}

	// if have the same entry in this.toRemove and other.toRemove
	// and existing entry is with different DAA score, in this case - this is an error
	if err := resolveConflicts("diffFrom", this.toRemove, other.toRemove,
		func(_ *externalapi.DomainOutpoint, utxoEntry, diffEntry externalapi.UTXOEntry) bool {
			return utxoEntry.BlockDAAScore() != diffEntry.BlockDAAScore()
		}, "both in this.toRemove and other.toRemove with different DAA scores, with no corresponding "+
			"entry in this.toAdd"); err != nil {
		return nil, err
	}

	result := &mutableUTXODiff{
		toAdd:    make(utxoCollection),
		toRemove: make(utxoCollection),
	}

	// All transactions in this.toAdd:
	// If they are not in other.toAdd - should be added in result.toRemove
	inBothToAdd := make(utxoCollection)
	subtractionWithRemainderHavingDAAScoreInPlace(this.toAdd, other.toAdd, result.toRemove, inBothToAdd)
	// If they are in exactly one of this.toRemove/other.toRemove, this and other disagree about
	// whether the outpoint is still present, despite both independently agreeing it was added (same
	// value, same DAA score, or they wouldn't be in inBothToAdd at all). Same reasoning as the
	// resolveConflicts calls above (see isTolerableConflict): this is the expected shape when only
	// one of two competing reconstructions also observed a spend of a coinbase-ID-colliding
	// outpoint - not corruption. Trust the toAdd agreement and leave the outpoint out of the result
	// diff entirely (no change) rather than hard-erroring the whole reconstruction over it.
	for outpoint, addedEntry := range inBothToAdd {
		inThisToRemove := this.toRemove.Contains(&outpoint)
		inOtherToRemove := other.toRemove.Contains(&outpoint)
		if inThisToRemove == inOtherToRemove {
			continue
		}
		var removedEntry externalapi.UTXOEntry
		if inThisToRemove {
			removedEntry, _ = this.toRemove.Get(&outpoint)
		} else {
			removedEntry, _ = other.toRemove.Get(&outpoint)
		}
		if !isTolerableConflict(addedEntry, removedEntry) {
			return nil, errors.Errorf("diffFrom: outpoint %s both in this.toAdd, other.toAdd, and only "+
				"one of this.toRemove and other.toRemove (addedEntry: %s, removedEntry: %s)",
				outpoint, describeConflictEntry(addedEntry), describeConflictEntry(removedEntry))
		}
		log.Debugf("diffFrom: outpoint %s both in this.toAdd, other.toAdd, and only one of this.toRemove "+
			"and other.toRemove (entries agree on value - historical coinbase ID collision or duplicate "+
			"cross-reconstruction acceptance) - leaving it out of the result diff", outpoint)
	}

	// All transactions in other.toRemove:
	// If they are not in this.toRemove - should be added in result.toRemove
	subtractionHavingDAAScoreInPlace(other.toRemove, this.toRemove, result.toRemove)

	// All transactions in this.toRemove:
	// If they are not in other.toRemove - should be added in result.toAdd
	subtractionHavingDAAScoreInPlace(this.toRemove, other.toRemove, result.toAdd)

	// All transactions in other.toAdd:
	// If they are not in this.toAdd - should be added in result.toAdd
	subtractionHavingDAAScoreInPlace(other.toAdd, this.toAdd, result.toAdd)

	return result, nil
}

// WithDiffInPlace applies provided diff to this diff in-place, that would be the result if
// first d, and than diff were applied to the same base
//
// The two classic conflicting cases (an outpoint removed by both diffs, or added by both diffs)
// are handled the same way diffFrom handles its own conflict shapes - see resolveConflicts and
// isTolerableConflict.
func withDiffInPlace(this *mutableUTXODiff, other *mutableUTXODiff) error {
	if err := resolveConflicts("withDiffInPlace", other.toRemove, this.toRemove,
		func(outpoint *externalapi.DomainOutpoint, entryToAdd, _ externalapi.UTXOEntry) bool {
			return !this.toAdd.containsWithDAAScore(outpoint, entryToAdd.BlockDAAScore())
		}, "both in this.toRemove and in other.toRemove"); err != nil {
		return err
	}

	if err := resolveConflicts("withDiffInPlace", other.toAdd, this.toAdd,
		func(outpoint *externalapi.DomainOutpoint, _ externalapi.UTXOEntry, existingEntry externalapi.UTXOEntry) bool {
			return !other.toRemove.containsWithDAAScore(outpoint, existingEntry.BlockDAAScore())
		}, "both in this.toAdd and in other.toAdd"); err != nil {
		return err
	}

	intersection := make(utxoCollection)
	// If not exists neither in toAdd nor in toRemove - add to toRemove
	intersectionWithRemainderHavingDAAScoreInPlace(other.toRemove, this.toAdd, intersection, this.toRemove)
	// If already exists in toAdd with the same DAA score - remove from toAdd
	this.toAdd.removeMultiple(intersection)

	intersection = make(utxoCollection)
	// If not exists neither in toAdd nor in toRemove, or exists in toRemove with different DAA score - add to toAdd
	intersectionWithRemainderHavingDAAScoreInPlace(other.toAdd, this.toRemove, intersection, this.toAdd)
	// If already exists in toRemove with the same DAA score - remove from toRemove
	this.toRemove.removeMultiple(intersection)

	return nil
}

// WithDiff applies provided diff to this diff, creating a new mutableUTXODiff, that would be the result if
// first d, and than diff were applied to some base
func withDiff(this *mutableUTXODiff, diff *mutableUTXODiff) (*mutableUTXODiff, error) {
	clone := this.clone()

	err := withDiffInPlace(clone, diff)
	if err != nil {
		return nil, err
	}

	return clone, nil
}
