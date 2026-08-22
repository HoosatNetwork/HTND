package utxo

import (
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

// DiffFrom returns a new mutableUTXODiff with the difference between this mutableUTXODiff and another.
//
// Assumes that both diffs are from the same base when possible.
// When the base assumption is violated (reorgs, pruning, virtual-genesis, header-only blocks, etc.)
// the classic "both in this.toAdd and other.toRemove" situations are treated as cancellations
// instead of hard errors. The conflicting outpoints are removed and a warning is logged.
//
// The 3×3 table is still respected for the normal cases:
//
// .........|...........| this      |...........|...........
// ---------+-----------+-----------+-----------+-----------
// .........|...........| toAdd     | toRemove  | None
// ---------+-----------+-----------+-----------+-----------
// other    | toAdd     | -         | cancel    | toAdd
// ---------+-----------+-----------+-----------+-----------
// .........| toRemove  | cancel    | -         | toRemove
// ---------+-----------+-----------+-----------+-----------
// .........| None      | toRemove  | toAdd     | -
func diffFrom(this, other *mutableUTXODiff) (*mutableUTXODiff, error) {
	// ------------------------------------------------------------------
	// 1. Detect and cancel the two classic conflicting cases
	// ------------------------------------------------------------------

	// Case A: outpoint in this.toRemove AND other.toAdd
	isNotAddedOutputRemovedWithDAAScore := func(outpoint *externalapi.DomainOutpoint, utxoEntry, diffEntry externalapi.UTXOEntry) bool {
		return !(diffEntry.BlockDAAScore() != utxoEntry.BlockDAAScore() &&
			(this.toAdd.containsWithDAAScore(outpoint, diffEntry.BlockDAAScore()) ||
				other.toRemove.containsWithDAAScore(outpoint, utxoEntry.BlockDAAScore())))
	}

	if offendingOutpoint, ok := checkIntersectionWithRule(this.toRemove, other.toAdd, isNotAddedOutputRemovedWithDAAScore); ok {
		log.Warnf("diffFrom: outpoint %s both in this.toRemove and in other.toAdd "+
			"(incompatible bases / DAA-score mismatch) – treating as cancelled", offendingOutpoint)

		// Cancel the conflict so it cannot pollute the result
		delete(this.toRemove, *offendingOutpoint)
		delete(other.toAdd, *offendingOutpoint)
	}

	// Case B: outpoint in this.toAdd AND other.toRemove
	isNotRemovedOutputAddedWithDAAScore := func(outpoint *externalapi.DomainOutpoint, utxoEntry, diffEntry externalapi.UTXOEntry) bool {
		return !(diffEntry.BlockDAAScore() != utxoEntry.BlockDAAScore() &&
			(this.toRemove.containsWithDAAScore(outpoint, diffEntry.BlockDAAScore()) ||
				other.toAdd.containsWithDAAScore(outpoint, utxoEntry.BlockDAAScore())))
	}

	if offendingOutpoint, ok := checkIntersectionWithRule(this.toAdd, other.toRemove, isNotRemovedOutputAddedWithDAAScore); ok {
		log.Warnf("diffFrom: outpoint %s both in this.toAdd and in other.toRemove "+
			"(incompatible bases / DAA-score mismatch) – treating as cancelled", offendingOutpoint)

		// Cancel the conflict
		delete(this.toAdd, *offendingOutpoint)
		delete(other.toRemove, *offendingOutpoint)
	}

	// ------------------------------------------------------------------
	// 2. Same outpoint in both toRemove collections with different DAA scores
	//    (still treated as a hard error – this is almost always real corruption)
	// ------------------------------------------------------------------
	if offendingOutpoint, ok := checkIntersectionWithRule(this.toRemove, other.toRemove,
		func(_ *externalapi.DomainOutpoint, utxoEntry, diffEntry externalapi.UTXOEntry) bool {
			return utxoEntry.BlockDAAScore() != diffEntry.BlockDAAScore()
		}); ok {
		return nil, errors.Errorf("diffFrom: outpoint %s both in this.toRemove and other.toRemove with different "+
			"DAA scores, with no corresponding entry in this.toAdd", offendingOutpoint)
	}

	// ------------------------------------------------------------------
	// 3. Build the result using the normal algebra
	// ------------------------------------------------------------------
	result := &mutableUTXODiff{
		toAdd:    make(utxoCollection),
		toRemove: make(utxoCollection),
	}

	// All transactions in this.toAdd:
	// If they are not in other.toAdd - should be added in result.toRemove
	inBothToAdd := make(utxoCollection)
	subtractionWithRemainderHavingDAAScoreInPlace(this.toAdd, other.toAdd, result.toRemove, inBothToAdd)

	// Safety check that should now almost never fire after the cancellations above
	if checkIntersection(inBothToAdd, this.toRemove) != checkIntersection(inBothToAdd, other.toRemove) {
		log.Warnf("diffFrom: residual inconsistency after cancellation – outpoint both in this.toAdd, other.toAdd, " +
			"and only one of this.toRemove / other.toRemove. Returning best-effort result.")
		// We continue instead of failing hard
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
func withDiffInPlace(this *mutableUTXODiff, other *mutableUTXODiff) error {
	if offendingOutpoint, ok := checkIntersectionWithRule(other.toRemove, this.toRemove,
		func(outpoint *externalapi.DomainOutpoint, entryToAdd, _ externalapi.UTXOEntry) bool {
			return !this.toAdd.containsWithDAAScore(outpoint, entryToAdd.BlockDAAScore())
		}); ok {
		return errors.Errorf(
			"withDiffInPlace: outpoint %s both in this.toRemove and in other.toRemove", offendingOutpoint)
	}

	if offendingOutpoint, ok := checkIntersectionWithRule(other.toAdd, this.toAdd,
		func(outpoint *externalapi.DomainOutpoint, _ externalapi.UTXOEntry, existingEntry externalapi.UTXOEntry) bool {
			return !other.toRemove.containsWithDAAScore(outpoint, existingEntry.BlockDAAScore())
		}); ok {
		return errors.Errorf(
			"withDiffInPlace: outpoint %s both in this.toAdd and in other.toAdd", offendingOutpoint)
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
