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

	if offendingOutpoint, ok := checkIntersectionWithRule(this.toRemove, other.toAdd, isNotAddedOutputRemovedWithDAAScore); ok {
		return nil, errors.Errorf("diffFrom: outpoint %s both in this.toRemove and in other.toAdd", offendingOutpoint)
	}

	// check that NOT (entries with unequal DAA score AND utxoEntry is in this.toRemove and/or other.toAdd) -> Error
	isNotRemovedOutputAddedWithDAAScore := func(outpoint *externalapi.DomainOutpoint, utxoEntry, diffEntry externalapi.UTXOEntry) bool {
		return !(diffEntry.BlockDAAScore() != utxoEntry.BlockDAAScore() &&
			(this.toRemove.containsWithDAAScore(outpoint, diffEntry.BlockDAAScore()) ||
				other.toAdd.containsWithDAAScore(outpoint, utxoEntry.BlockDAAScore())))
	}

	if offendingOutpoint, ok := checkIntersectionWithRule(this.toAdd, other.toRemove, isNotRemovedOutputAddedWithDAAScore); ok {
		return nil, errors.Errorf("diffFrom: outpoint %s both in this.toAdd and in other.toRemove", offendingOutpoint)
	}

	// if have the same entry in this.toRemove and other.toRemove
	// and existing entry is with different DAA score, in this case - this is an error
	if offendingOutpoint, ok := checkIntersectionWithRule(this.toRemove, other.toRemove,
		func(_ *externalapi.DomainOutpoint, utxoEntry, diffEntry externalapi.UTXOEntry) bool {
			return utxoEntry.BlockDAAScore() != diffEntry.BlockDAAScore()
		}); ok {
		return nil, errors.Errorf("diffFrom: outpoint %s both in this.toRemove and other.toRemove with different "+
			"DAA scores, with no corresponding entry in this.toAdd", offendingOutpoint)
	}

	result := &mutableUTXODiff{
		toAdd:    make(utxoCollection),
		toRemove: make(utxoCollection),
	}

	// All transactions in this.toAdd:
	// If they are not in other.toAdd - should be added in result.toRemove
	inBothToAdd := make(utxoCollection)
	subtractionWithRemainderHavingDAAScoreInPlace(this.toAdd, other.toAdd, result.toRemove, inBothToAdd)
	// If they are in other.toRemove - base utxoSet is not the same
	if checkIntersection(inBothToAdd, this.toRemove) != checkIntersection(inBothToAdd, other.toRemove) {
		return nil, errors.New(
			"diffFrom: outpoint both in this.toAdd, other.toAdd, and only one of this.toRemove and other.toRemove")
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
// can legitimately happen against real chain data - most notably historical coinbase transactions
// whose IDs collided before per-block entropy was folded into the payload (see
// coinbaseEntropyActivationVersion). That fix only protects blocks mined after the hard fork, so
// already-mined colliding blocks stay in the chain forever. Rather than hard-failing UTXO
// reconstruction for every node walking back over such a block, these conflicts are only logged
// here; the outpoints are left untouched and fall through to the merge algebra below, which
// already collapses a doubly-removed/doubly-added outpoint down to a single, consistent entry.
func withDiffInPlace(this *mutableUTXODiff, other *mutableUTXODiff) error {
	if offendingOutpoint, ok := checkIntersectionWithRule(other.toRemove, this.toRemove,
		func(outpoint *externalapi.DomainOutpoint, entryToAdd, _ externalapi.UTXOEntry) bool {
			return !this.toAdd.containsWithDAAScore(outpoint, entryToAdd.BlockDAAScore())
		}); ok {
		log.Warnf("withDiffInPlace: outpoint %s both in this.toRemove and in other.toRemove "+
			"(likely a historical coinbase ID collision) - leaving it removed", offendingOutpoint)
	}

	if offendingOutpoint, ok := checkIntersectionWithRule(other.toAdd, this.toAdd,
		func(outpoint *externalapi.DomainOutpoint, _ externalapi.UTXOEntry, existingEntry externalapi.UTXOEntry) bool {
			return !other.toRemove.containsWithDAAScore(outpoint, existingEntry.BlockDAAScore())
		}); ok {
		log.Warnf("withDiffInPlace: outpoint %s both in this.toAdd and in other.toAdd "+
			"(likely a historical coinbase ID collision) - leaving it added", offendingOutpoint)
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
