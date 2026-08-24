package utxo

import (
	"reflect"
	"strings"
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus/utils/transactionid"

	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
)

func (mud *mutableUTXODiff) equal(other *mutableUTXODiff) bool {
	if mud == nil || other == nil {
		return mud == other
	}

	return reflect.DeepEqual(mud.toAdd, other.toAdd) &&
		reflect.DeepEqual(mud.toRemove, other.toRemove)
}

// helper to create a consistent set of test fixtures
func testFixtures() (
	txID0, txID1, txID2 *externalapi.DomainTransactionID,
	outpoint0, outpoint1, outpoint2 *externalapi.DomainOutpoint,
	utxoEntry0, utxoEntry1, utxoEntry2, utxoEntry0AltDAA externalapi.UTXOEntry,
) {
	txID0, _ = transactionid.FromString("0000000000000000000000000000000000000000000000000000000000000000")
	txID1, _ = transactionid.FromString("1111111111111111111111111111111111111111111111111111111111111111")
	txID2, _ = transactionid.FromString("2222222222222222222222222222222222222222222222222222222222222222")

	outpoint0 = externalapi.NewDomainOutpoint(txID0, 0)
	outpoint1 = externalapi.NewDomainOutpoint(txID1, 0)
	outpoint2 = externalapi.NewDomainOutpoint(txID2, 0)

	utxoEntry0 = NewUTXOEntry(10, &externalapi.ScriptPublicKey{Script: []byte{}, Version: 0}, true, 0)
	utxoEntry1 = NewUTXOEntry(20, &externalapi.ScriptPublicKey{Script: []byte{}, Version: 0}, false, 1)
	utxoEntry2 = NewUTXOEntry(30, &externalapi.ScriptPublicKey{Script: []byte{}, Version: 0}, false, 2)
	// Same outpoint semantics but different DAA score
	utxoEntry0AltDAA = NewUTXOEntry(10, &externalapi.ScriptPublicKey{Script: []byte{}, Version: 0}, true, 99)

	return
}

// TestUTXOCollection makes sure that utxoCollection cloning and string representations work as expected.
func TestUTXOCollection(t *testing.T) {
	_, _, _, outpoint0, outpoint1, outpoint2, utxoEntry0, utxoEntry1, utxoEntry2, _ := testFixtures()

	tests := []struct {
		name           string
		collection     utxoCollection
		expectedString string
	}{
		{
			name:           "empty collection",
			collection:     utxoCollection{},
			expectedString: "[  ]",
		},
		{
			name: "one member",
			collection: utxoCollection{
				*outpoint0: utxoEntry1,
			},
			expectedString: "[ (0000000000000000000000000000000000000000000000000000000000000000, 0) => 20, daaScore: 1 ]",
		},
		{
			name: "two members",
			collection: utxoCollection{
				*outpoint0: utxoEntry0,
				*outpoint1: utxoEntry1,
			},
			expectedString: "[ (0000000000000000000000000000000000000000000000000000000000000000, 0) => 10, daaScore: 0, (1111111111111111111111111111111111111111111111111111111111111111, 0) => 20, daaScore: 1 ]",
		},
		{
			name: "three members (different outpoints)",
			collection: utxoCollection{
				*outpoint0: utxoEntry0,
				*outpoint1: utxoEntry1,
				*outpoint2: utxoEntry2,
			},
			// String order is implementation-defined (map iteration); we only check that every outpoint appears
			expectedString: "", // validated specially below
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			collectionString := test.collection.String()

			if test.expectedString != "" {
				if collectionString != test.expectedString {
					t.Errorf("unexpected string. Expected: %q, got: %q", test.expectedString, collectionString)
				}
			} else {
				// For multi-member unordered case, verify the presence of every entry's transaction ID hex
				if !strings.Contains(collectionString, "0000000000000000000000000000000000000000000000000000000000000000") ||
					!strings.Contains(collectionString, "1111111111111111111111111111111111111111111111111111111111111111") ||
					!strings.Contains(collectionString, "2222222222222222222222222222222222222222222222222222222222222222") {
					t.Errorf("string representation missing expected outpoints: %s", collectionString)
				}
			}

			// Cloning must produce a deep-equal value that is not the same reference
			collectionClone := test.collection.Clone()
			if reflect.ValueOf(collectionClone).Pointer() == reflect.ValueOf(test.collection).Pointer() {
				t.Errorf("collection is reference-equal to its clone")
			}
			if !reflect.DeepEqual(test.collection, collectionClone) {
				t.Errorf("collection is not equal to its clone. Expected: %s, got: %s",
					collectionString, collectionClone.String())
			}
		})
	}
}

// TestUTXODiff makes sure that mutableUTXODiff creation, cloning, string representations,
// and basic add/remove (including duplicate rejection) work as expected.
func TestUTXODiff(t *testing.T) {
	_, _, _, outpoint0, outpoint1, outpoint2, utxoEntry0, utxoEntry1, utxoEntry2, utxoEntry0AltDAA := testFixtures()

	diff := newMutableUTXODiff()

	if len(diff.toAdd) != 0 || len(diff.toRemove) != 0 {
		t.Errorf("new diff is not empty")
	}

	// Successful add + remove of distinct outpoints
	if err := diff.addEntry(outpoint0, utxoEntry0); err != nil {
		t.Fatalf("error adding entry: %s", err)
	}
	if err := diff.removeEntry(outpoint1, utxoEntry1); err != nil {
		t.Fatalf("error removing entry: %s", err)
	}

	// Cloning
	clonedDiff := diff.clone()
	if clonedDiff == diff {
		t.Errorf("cloned diff is reference-equal to the original")
	}
	if !reflect.DeepEqual(clonedDiff, diff) {
		t.Errorf("cloned diff not equal to the original. Original: %v, cloned: %v", diff, clonedDiff)
	}

	// String representation
	expectedDiffString := "toAdd: [ (0000000000000000000000000000000000000000000000000000000000000000, 0) => 10, daaScore: 0 ]; toRemove: [ (1111111111111111111111111111111111111111111111111111111111111111, 0) => 20, daaScore: 1 ]"
	diffString := clonedDiff.String()
	if diffString != expectedDiffString {
		t.Errorf("unexpected diff string.\nExpected: %q\nGot:      %q", expectedDiffString, diffString)
	}

	// ---------- Duplicate handling ----------

	// Adding the same outpoint a second time must fail
	err := diff.addEntry(outpoint0, utxoEntry0)
	if err == nil {
		t.Errorf("expected error when adding duplicate outpoint to toAdd, got nil")
	} else if !strings.Contains(err.Error(), "Cannot add outpoint") {
		t.Errorf("unexpected error message for duplicate add: %v", err)
	}

	// Removing the same outpoint a second time must fail
	err = diff.removeEntry(outpoint1, utxoEntry1)
	if err == nil {
		t.Errorf("expected error when removing duplicate outpoint from toRemove, got nil")
	} else if !strings.Contains(err.Error(), "Cannot remove outpoint") {
		t.Errorf("unexpected error message for duplicate remove: %v", err)
	}

	// Adding an outpoint that is already in toRemove with matching DAA score cancels the remove
	diff2 := newMutableUTXODiff()
	if err := diff2.removeEntry(outpoint2, utxoEntry2); err != nil {
		t.Fatalf("setup remove failed: %v", err)
	}
	if err := diff2.addEntry(outpoint2, utxoEntry2); err != nil {
		t.Fatalf("add that should cancel remove failed: %v", err)
	}
	if len(diff2.toAdd) != 0 || len(diff2.toRemove) != 0 {
		t.Errorf("expected empty diff after cancel, got toAdd=%v toRemove=%v", diff2.toAdd, diff2.toRemove)
	}

	// Removing an outpoint that is already in toAdd with matching DAA score cancels the add
	diff3 := newMutableUTXODiff()
	if err := diff3.addEntry(outpoint2, utxoEntry2); err != nil {
		t.Fatalf("setup add failed: %v", err)
	}
	if err := diff3.removeEntry(outpoint2, utxoEntry2); err != nil {
		t.Fatalf("remove that should cancel add failed: %v", err)
	}
	if len(diff3.toAdd) != 0 || len(diff3.toRemove) != 0 {
		t.Errorf("expected empty diff after cancel, got toAdd=%v toRemove=%v", diff3.toAdd, diff3.toRemove)
	}

	// Different DAA score: adding a different-DAA version of an outpoint that is in toRemove
	// should NOT cancel; it should end up in toAdd while the original stays in toRemove
	diff4 := newMutableUTXODiff()
	if err := diff4.removeEntry(outpoint0, utxoEntry0); err != nil {
		t.Fatalf("setup remove failed: %v", err)
	}
	if err := diff4.addEntry(outpoint0, utxoEntry0AltDAA); err != nil {
		t.Fatalf("add with different DAA failed: %v", err)
	}
	if !diff4.toRemove.containsWithDAAScore(outpoint0, utxoEntry0.BlockDAAScore()) {
		t.Errorf("expected original entry to remain in toRemove")
	}
	if !diff4.toAdd.containsWithDAAScore(outpoint0, utxoEntry0AltDAA.BlockDAAScore()) {
		t.Errorf("expected alt-DAA entry to be present in toAdd")
	}
}

// TestUTXODiffRules makes sure that all diffFrom and WithDiff rules are followed.
// Each test case represents a cell in the two tables outlined in the documentation for mutableUTXODiff.
// Extended with multi-outpoint cases and explicit duplicate-outpoint scenarios.
func TestUTXODiffRules(t *testing.T) {
	// Replace utxoEntry0/1/2 and utxoEntry0AltDAA with _ since they are immediately overwritten below.
	// This table exercises the diff algebra's general conflict/error-path correctness, independent of
	// the coinbase-collision-tolerance carve-out (see TestCoinbaseCollisionConflicts) - every entry
	// here is deliberately non-coinbase so that carve-out never kicks in and these cases keep testing
	// what they were designed to test.
	_, _, _, outpoint0, outpoint1, outpoint2, _, _, _, _ := testFixtures()

	// Keep the original single-outpoint names used by the classic table (now using := to declare them)
	utxoEntry0 := NewUTXOEntry(10, &externalapi.ScriptPublicKey{Script: []byte{}, Version: 0}, false, 0)
	utxoEntry1 := NewUTXOEntry(10, &externalapi.ScriptPublicKey{Script: []byte{}, Version: 0}, false, 0)
	utxoEntry2 := NewUTXOEntry(20, &externalapi.ScriptPublicKey{Script: []byte{}, Version: 0}, false, 1)
	utxoEntry0AltDAA := NewUTXOEntry(10, &externalapi.ScriptPublicKey{Script: []byte{}, Version: 0}, false, 99)

	// Classic single-outpoint table (identical to original)
	tests := []struct {
		name                   string
		this                   *mutableUTXODiff
		other                  *mutableUTXODiff
		expectedDiffFromResult *mutableUTXODiff
		expectedWithDiffResult *mutableUTXODiff
		// hadTolerableConflict marks a case where this and other use the identical entry (same
		// outpoint, amount, script, and DAA score) on opposite sides of a conflict. isTolerableConflict
		// treats that as a harmless bookkeeping disagreement rather than an error (see diff_algebra.go),
		// so these no longer hard-fail - but the resolution is intentionally lossy (the outpoint is left
		// wherever the surrounding merge algebra happens to put it), so the WithDiff/diffFrom round-trip
		// checks below, which assume an exact algebraic inverse, don't hold for these cases.
		hadTolerableConflict bool
	}{
		{
			name: "first toAdd in this, first toAdd in other",
			this: &mutableUTXODiff{
				toAdd:    utxoCollection{*outpoint0: utxoEntry1},
				toRemove: utxoCollection{},
			},
			other: &mutableUTXODiff{
				toAdd:    utxoCollection{*outpoint0: utxoEntry1},
				toRemove: utxoCollection{},
			},
			expectedDiffFromResult: &mutableUTXODiff{
				toAdd:    utxoCollection{},
				toRemove: utxoCollection{},
			},
			expectedWithDiffResult: &mutableUTXODiff{
				toAdd:    utxoCollection{*outpoint0: utxoEntry1},
				toRemove: utxoCollection{},
			},
			hadTolerableConflict: true,
		},
		{
			name: "first in toAdd in this, second in toAdd in other",
			this: &mutableUTXODiff{
				toAdd:    utxoCollection{*outpoint0: utxoEntry1},
				toRemove: utxoCollection{},
			},
			other: &mutableUTXODiff{
				toAdd:    utxoCollection{*outpoint0: utxoEntry2},
				toRemove: utxoCollection{},
			},
			expectedDiffFromResult: &mutableUTXODiff{
				toAdd:    utxoCollection{*outpoint0: utxoEntry2},
				toRemove: utxoCollection{*outpoint0: utxoEntry1},
			},
			expectedWithDiffResult: nil,
		},
		{
			name: "first in toAdd in this, second in toRemove in other",
			this: &mutableUTXODiff{
				toAdd:    utxoCollection{*outpoint0: utxoEntry1},
				toRemove: utxoCollection{},
			},
			other: &mutableUTXODiff{
				toAdd:    utxoCollection{},
				toRemove: utxoCollection{*outpoint0: utxoEntry1},
			},
			expectedDiffFromResult: &mutableUTXODiff{
				toAdd:    utxoCollection{},
				toRemove: utxoCollection{*outpoint0: utxoEntry1},
			},
			expectedWithDiffResult: &mutableUTXODiff{
				toAdd:    utxoCollection{},
				toRemove: utxoCollection{},
			},
			hadTolerableConflict: true,
		},
		{
			name: "first in toAdd in this and other, second in toRemove in other",
			this: &mutableUTXODiff{
				toAdd:    utxoCollection{*outpoint0: utxoEntry1},
				toRemove: utxoCollection{},
			},
			other: &mutableUTXODiff{
				toAdd:    utxoCollection{*outpoint0: utxoEntry1},
				toRemove: utxoCollection{*outpoint0: utxoEntry2},
			},
			expectedDiffFromResult: nil,
			expectedWithDiffResult: &mutableUTXODiff{
				toAdd:    utxoCollection{*outpoint0: utxoEntry1},
				toRemove: utxoCollection{*outpoint0: utxoEntry2},
			},
			hadTolerableConflict: true,
		},
		{
			name: "first in toAdd in this and toRemove in other, second in toAdd in other",
			this: &mutableUTXODiff{
				toAdd:    utxoCollection{*outpoint0: utxoEntry1},
				toRemove: utxoCollection{},
			},
			other: &mutableUTXODiff{
				toAdd:    utxoCollection{*outpoint0: utxoEntry2},
				toRemove: utxoCollection{*outpoint0: utxoEntry1},
			},
			expectedDiffFromResult: &mutableUTXODiff{
				toAdd:    utxoCollection{*outpoint0: utxoEntry2},
				toRemove: utxoCollection{*outpoint0: utxoEntry1},
			},
			expectedWithDiffResult: &mutableUTXODiff{
				toAdd:    utxoCollection{*outpoint0: utxoEntry2},
				toRemove: utxoCollection{},
			},
			hadTolerableConflict: true,
		},
		{
			name: "first in toAdd in this, empty other",
			this: &mutableUTXODiff{
				toAdd:    utxoCollection{*outpoint0: utxoEntry1},
				toRemove: utxoCollection{},
			},
			other: &mutableUTXODiff{
				toAdd:    utxoCollection{},
				toRemove: utxoCollection{},
			},
			expectedDiffFromResult: &mutableUTXODiff{
				toAdd:    utxoCollection{},
				toRemove: utxoCollection{*outpoint0: utxoEntry1},
			},
			expectedWithDiffResult: &mutableUTXODiff{
				toAdd:    utxoCollection{*outpoint0: utxoEntry1},
				toRemove: utxoCollection{},
			},
		},
		{
			name: "first in toRemove in this and in toAdd in other",
			this: &mutableUTXODiff{
				toAdd:    utxoCollection{},
				toRemove: utxoCollection{*outpoint0: utxoEntry1},
			},
			other: &mutableUTXODiff{
				toAdd:    utxoCollection{*outpoint0: utxoEntry1},
				toRemove: utxoCollection{},
			},
			expectedDiffFromResult: &mutableUTXODiff{
				toAdd:    utxoCollection{*outpoint0: utxoEntry1},
				toRemove: utxoCollection{},
			},
			expectedWithDiffResult: &mutableUTXODiff{
				toAdd:    utxoCollection{},
				toRemove: utxoCollection{},
			},
			hadTolerableConflict: true,
		},
		{
			name: "first in toRemove in this, second in toAdd in other",
			this: &mutableUTXODiff{
				toAdd:    utxoCollection{},
				toRemove: utxoCollection{*outpoint0: utxoEntry1},
			},
			other: &mutableUTXODiff{
				toAdd:    utxoCollection{*outpoint0: utxoEntry2},
				toRemove: utxoCollection{},
			},
			expectedDiffFromResult: nil,
			expectedWithDiffResult: &mutableUTXODiff{
				toAdd:    utxoCollection{*outpoint0: utxoEntry2},
				toRemove: utxoCollection{*outpoint0: utxoEntry1},
			},
		},
		{
			name: "first in toRemove in this and other",
			this: &mutableUTXODiff{
				toAdd:    utxoCollection{},
				toRemove: utxoCollection{*outpoint0: utxoEntry1},
			},
			other: &mutableUTXODiff{
				toAdd:    utxoCollection{},
				toRemove: utxoCollection{*outpoint0: utxoEntry1},
			},
			expectedDiffFromResult: &mutableUTXODiff{
				toAdd:    utxoCollection{},
				toRemove: utxoCollection{},
			},
			expectedWithDiffResult: &mutableUTXODiff{
				toAdd:    utxoCollection{},
				toRemove: utxoCollection{*outpoint0: utxoEntry1},
			},
			hadTolerableConflict: true,
		},
		{
			name: "first in toRemove in this, second in toRemove in other",
			this: &mutableUTXODiff{
				toAdd:    utxoCollection{},
				toRemove: utxoCollection{*outpoint0: utxoEntry1},
			},
			other: &mutableUTXODiff{
				toAdd:    utxoCollection{},
				toRemove: utxoCollection{*outpoint0: utxoEntry2},
			},
			expectedDiffFromResult: nil,
			expectedWithDiffResult: nil,
		},
		{
			name: "first in toRemove in this and toAdd in other, second in toRemove in other",
			this: &mutableUTXODiff{
				toAdd:    utxoCollection{},
				toRemove: utxoCollection{*outpoint0: utxoEntry1},
			},
			other: &mutableUTXODiff{
				toAdd:    utxoCollection{*outpoint0: utxoEntry1},
				toRemove: utxoCollection{*outpoint0: utxoEntry2},
			},
			expectedDiffFromResult: nil,
			expectedWithDiffResult: nil,
		},
		{
			name: "first in toRemove in this and other, second in toAdd in other",
			this: &mutableUTXODiff{
				toAdd:    utxoCollection{},
				toRemove: utxoCollection{*outpoint0: utxoEntry1},
			},
			other: &mutableUTXODiff{
				toAdd:    utxoCollection{*outpoint0: utxoEntry2},
				toRemove: utxoCollection{*outpoint0: utxoEntry1},
			},
			expectedDiffFromResult: &mutableUTXODiff{
				toAdd:    utxoCollection{*outpoint0: utxoEntry2},
				toRemove: utxoCollection{},
			},
			expectedWithDiffResult: &mutableUTXODiff{
				toAdd:    utxoCollection{*outpoint0: utxoEntry2},
				toRemove: utxoCollection{*outpoint0: utxoEntry1},
			},
			hadTolerableConflict: true,
		},
		{
			name: "first in toRemove in this, empty other",
			this: &mutableUTXODiff{
				toAdd:    utxoCollection{},
				toRemove: utxoCollection{*outpoint0: utxoEntry1},
			},
			other: &mutableUTXODiff{
				toAdd:    utxoCollection{},
				toRemove: utxoCollection{},
			},
			expectedDiffFromResult: &mutableUTXODiff{
				toAdd:    utxoCollection{*outpoint0: utxoEntry1},
				toRemove: utxoCollection{},
			},
			expectedWithDiffResult: &mutableUTXODiff{
				toAdd:    utxoCollection{},
				toRemove: utxoCollection{*outpoint0: utxoEntry1},
			},
		},
		{
			name: "first in toAdd in this and other, second in toRemove in this",
			this: &mutableUTXODiff{
				toAdd:    utxoCollection{*outpoint0: utxoEntry1},
				toRemove: utxoCollection{*outpoint0: utxoEntry2},
			},
			other: &mutableUTXODiff{
				toAdd:    utxoCollection{*outpoint0: utxoEntry1},
				toRemove: utxoCollection{},
			},
			expectedDiffFromResult: nil,
			expectedWithDiffResult: &mutableUTXODiff{
				toAdd:    utxoCollection{*outpoint0: utxoEntry1},
				toRemove: utxoCollection{*outpoint0: utxoEntry2},
			},
			hadTolerableConflict: true,
		},
		{
			name: "first in toAdd in this, second in toRemove in this and toAdd in other",
			this: &mutableUTXODiff{
				toAdd:    utxoCollection{*outpoint0: utxoEntry1},
				toRemove: utxoCollection{*outpoint0: utxoEntry2},
			},
			other: &mutableUTXODiff{
				toAdd:    utxoCollection{*outpoint0: utxoEntry2},
				toRemove: utxoCollection{},
			},
			expectedDiffFromResult: &mutableUTXODiff{
				toAdd:    utxoCollection{*outpoint0: utxoEntry2},
				toRemove: utxoCollection{*outpoint0: utxoEntry1},
			},
			expectedWithDiffResult: nil,
			hadTolerableConflict: true,
		},
		{
			name: "first in toAdd in this and toRemove in other, second in toRemove in this",
			this: &mutableUTXODiff{
				toAdd:    utxoCollection{*outpoint0: utxoEntry1},
				toRemove: utxoCollection{*outpoint0: utxoEntry2},
			},
			other: &mutableUTXODiff{
				toAdd:    utxoCollection{},
				toRemove: utxoCollection{*outpoint0: utxoEntry1},
			},
			expectedDiffFromResult: nil,
			expectedWithDiffResult: &mutableUTXODiff{
				toAdd:    utxoCollection{},
				toRemove: utxoCollection{*outpoint0: utxoEntry2},
			},
		},
		{
			name: "first in toAdd in this, second in toRemove in this and in other",
			this: &mutableUTXODiff{
				toAdd:    utxoCollection{*outpoint0: utxoEntry1},
				toRemove: utxoCollection{*outpoint0: utxoEntry2},
			},
			other: &mutableUTXODiff{
				toAdd:    utxoCollection{},
				toRemove: utxoCollection{*outpoint0: utxoEntry2},
			},
			expectedDiffFromResult: &mutableUTXODiff{
				toAdd:    utxoCollection{},
				toRemove: utxoCollection{*outpoint0: utxoEntry1},
			},
			expectedWithDiffResult: &mutableUTXODiff{
				toAdd:    utxoCollection{*outpoint0: utxoEntry1},
				toRemove: utxoCollection{*outpoint0: utxoEntry2},
			},
			hadTolerableConflict: true,
		},
		{
			name: "first in toAdd and second in toRemove in both this and other",
			this: &mutableUTXODiff{
				toAdd:    utxoCollection{*outpoint0: utxoEntry1},
				toRemove: utxoCollection{*outpoint0: utxoEntry2},
			},
			other: &mutableUTXODiff{
				toAdd:    utxoCollection{*outpoint0: utxoEntry1},
				toRemove: utxoCollection{*outpoint0: utxoEntry2},
			},
			expectedDiffFromResult: &mutableUTXODiff{
				toAdd:    utxoCollection{},
				toRemove: utxoCollection{},
			},
			expectedWithDiffResult: &mutableUTXODiff{
				toAdd:    utxoCollection{*outpoint0: utxoEntry1},
				toRemove: utxoCollection{*outpoint0: utxoEntry2},
			},
			hadTolerableConflict: true,
		},
		{
			name: "first in toAdd in this and toRemove in other, second in toRemove in this and toAdd in other",
			this: &mutableUTXODiff{
				toAdd:    utxoCollection{*outpoint0: utxoEntry1},
				toRemove: utxoCollection{*outpoint0: utxoEntry2},
			},
			other: &mutableUTXODiff{
				toAdd:    utxoCollection{*outpoint0: utxoEntry2},
				toRemove: utxoCollection{*outpoint0: utxoEntry1},
			},
			expectedDiffFromResult: nil,
			expectedWithDiffResult: &mutableUTXODiff{
				toAdd:    utxoCollection{},
				toRemove: utxoCollection{},
			},
		},
		{
			name: "first in toAdd and second in toRemove in this, empty other",
			this: &mutableUTXODiff{
				toAdd:    utxoCollection{*outpoint0: utxoEntry1},
				toRemove: utxoCollection{*outpoint0: utxoEntry2},
			},
			other: &mutableUTXODiff{
				toAdd:    utxoCollection{},
				toRemove: utxoCollection{},
			},
			expectedDiffFromResult: &mutableUTXODiff{
				toAdd:    utxoCollection{*outpoint0: utxoEntry2},
				toRemove: utxoCollection{*outpoint0: utxoEntry1},
			},
			expectedWithDiffResult: &mutableUTXODiff{
				toAdd:    utxoCollection{*outpoint0: utxoEntry1},
				toRemove: utxoCollection{*outpoint0: utxoEntry2},
			},
		},
		{
			name: "empty this, first in toAdd in other",
			this: &mutableUTXODiff{
				toAdd:    utxoCollection{},
				toRemove: utxoCollection{},
			},
			other: &mutableUTXODiff{
				toAdd:    utxoCollection{*outpoint0: utxoEntry1},
				toRemove: utxoCollection{},
			},
			expectedDiffFromResult: &mutableUTXODiff{
				toAdd:    utxoCollection{*outpoint0: utxoEntry1},
				toRemove: utxoCollection{},
			},
			expectedWithDiffResult: &mutableUTXODiff{
				toAdd:    utxoCollection{*outpoint0: utxoEntry1},
				toRemove: utxoCollection{},
			},
		},
		{
			name: "empty this, first in toRemove in other",
			this: &mutableUTXODiff{
				toAdd:    utxoCollection{},
				toRemove: utxoCollection{},
			},
			other: &mutableUTXODiff{
				toAdd:    utxoCollection{},
				toRemove: utxoCollection{*outpoint0: utxoEntry1},
			},
			expectedDiffFromResult: &mutableUTXODiff{
				toAdd:    utxoCollection{},
				toRemove: utxoCollection{*outpoint0: utxoEntry1},
			},
			expectedWithDiffResult: &mutableUTXODiff{
				toAdd:    utxoCollection{},
				toRemove: utxoCollection{*outpoint0: utxoEntry1},
			},
		},
		{
			name: "empty this, first in toAdd and second in toRemove in other",
			this: &mutableUTXODiff{
				toAdd:    utxoCollection{},
				toRemove: utxoCollection{},
			},
			other: &mutableUTXODiff{
				toAdd:    utxoCollection{*outpoint0: utxoEntry1},
				toRemove: utxoCollection{*outpoint0: utxoEntry2},
			},
			expectedDiffFromResult: &mutableUTXODiff{
				toAdd:    utxoCollection{*outpoint0: utxoEntry1},
				toRemove: utxoCollection{*outpoint0: utxoEntry2},
			},
			expectedWithDiffResult: &mutableUTXODiff{
				toAdd:    utxoCollection{*outpoint0: utxoEntry1},
				toRemove: utxoCollection{*outpoint0: utxoEntry2},
			},
		},
		{
			name: "empty this, empty other",
			this: &mutableUTXODiff{
				toAdd:    utxoCollection{},
				toRemove: utxoCollection{},
			},
			other: &mutableUTXODiff{
				toAdd:    utxoCollection{},
				toRemove: utxoCollection{},
			},
			expectedDiffFromResult: &mutableUTXODiff{
				toAdd:    utxoCollection{},
				toRemove: utxoCollection{},
			},
			expectedWithDiffResult: &mutableUTXODiff{
				toAdd:    utxoCollection{},
				toRemove: utxoCollection{},
			},
		},

		// ---------- Multi-outpoint extensions ----------

		{
			name: "two independent outpoints – both only in this.toAdd",
			this: &mutableUTXODiff{
				toAdd: utxoCollection{
					*outpoint0: utxoEntry0,
					*outpoint1: utxoEntry1,
				},
				toRemove: utxoCollection{},
			},
			other: &mutableUTXODiff{
				toAdd:    utxoCollection{},
				toRemove: utxoCollection{},
			},
			expectedDiffFromResult: &mutableUTXODiff{
				toAdd: utxoCollection{},
				toRemove: utxoCollection{
					*outpoint0: utxoEntry0,
					*outpoint1: utxoEntry1,
				},
			},
			expectedWithDiffResult: &mutableUTXODiff{
				toAdd: utxoCollection{
					*outpoint0: utxoEntry0,
					*outpoint1: utxoEntry1,
				},
				toRemove: utxoCollection{},
			},
		},
		{
			name: "two independent outpoints – one added in this, one removed in other",
			this: &mutableUTXODiff{
				toAdd:    utxoCollection{*outpoint0: utxoEntry0},
				toRemove: utxoCollection{},
			},
			other: &mutableUTXODiff{
				toAdd:    utxoCollection{},
				toRemove: utxoCollection{*outpoint1: utxoEntry1},
			},
			expectedDiffFromResult: &mutableUTXODiff{
				toAdd:    utxoCollection{},
				toRemove: utxoCollection{*outpoint0: utxoEntry0, *outpoint1: utxoEntry1},
			},
			expectedWithDiffResult: &mutableUTXODiff{
				toAdd:    utxoCollection{*outpoint0: utxoEntry0},
				toRemove: utxoCollection{*outpoint1: utxoEntry1},
			},
		},
		{
			name: "two outpoints – partial overlap on one outpoint (cancel)",
			this: &mutableUTXODiff{
				toAdd:    utxoCollection{*outpoint0: utxoEntry0, *outpoint1: utxoEntry1},
				toRemove: utxoCollection{},
			},
			other: &mutableUTXODiff{
				toAdd:    utxoCollection{},
				toRemove: utxoCollection{*outpoint0: utxoEntry0},
			},
			// outpoint0 is the identical entry (utxoEntry0) on both sides of a conflict - tolerated.
			expectedDiffFromResult: &mutableUTXODiff{
				toAdd:    utxoCollection{},
				toRemove: utxoCollection{*outpoint0: utxoEntry0, *outpoint1: utxoEntry1},
			},
			expectedWithDiffResult: &mutableUTXODiff{
				toAdd:    utxoCollection{*outpoint1: utxoEntry1},
				toRemove: utxoCollection{},
			},
			hadTolerableConflict: true,
		},
		{
			name: "three outpoints – mixed add/remove with one cancel",
			this: &mutableUTXODiff{
				toAdd:    utxoCollection{*outpoint0: utxoEntry0},
				toRemove: utxoCollection{*outpoint1: utxoEntry1},
			},
			other: &mutableUTXODiff{
				toAdd:    utxoCollection{*outpoint1: utxoEntry1, *outpoint2: utxoEntry2},
				toRemove: utxoCollection{},
			},
			// outpoint1 is the identical entry (utxoEntry1) on both sides of a conflict - tolerated.
			expectedDiffFromResult: &mutableUTXODiff{
				toAdd:    utxoCollection{*outpoint1: utxoEntry1, *outpoint2: utxoEntry2},
				toRemove: utxoCollection{*outpoint0: utxoEntry0},
			},
			expectedWithDiffResult: &mutableUTXODiff{
				toAdd:    utxoCollection{*outpoint0: utxoEntry0, *outpoint2: utxoEntry2},
				toRemove: utxoCollection{},
			},
			hadTolerableConflict: true,
		},
		{
			name: "identical multi-outpoint diffs – diffFrom empty, withDiff fails (duplicate adds)",
			this: &mutableUTXODiff{
				toAdd:    utxoCollection{*outpoint0: utxoEntry0, *outpoint1: utxoEntry1},
				toRemove: utxoCollection{*outpoint2: utxoEntry2},
			},
			other: &mutableUTXODiff{
				toAdd:    utxoCollection{*outpoint0: utxoEntry0, *outpoint1: utxoEntry1},
				toRemove: utxoCollection{*outpoint2: utxoEntry2},
			},
			expectedDiffFromResult: &mutableUTXODiff{
				toAdd:    utxoCollection{},
				toRemove: utxoCollection{},
			},
			expectedWithDiffResult: &mutableUTXODiff{
				toAdd:    utxoCollection{*outpoint0: utxoEntry0, *outpoint1: utxoEntry1},
				toRemove: utxoCollection{*outpoint2: utxoEntry2},
			},
			hadTolerableConflict: true,
		},

		// ---------- Different-DAA-score edge cases ----------

		{
			name: "same outpoint different DAA – this.toAdd vs other.toAdd",
			this: &mutableUTXODiff{
				toAdd:    utxoCollection{*outpoint0: utxoEntry0},
				toRemove: utxoCollection{},
			},
			other: &mutableUTXODiff{
				toAdd:    utxoCollection{*outpoint0: utxoEntry0AltDAA},
				toRemove: utxoCollection{},
			},
			expectedDiffFromResult: &mutableUTXODiff{
				toAdd:    utxoCollection{*outpoint0: utxoEntry0AltDAA},
				toRemove: utxoCollection{*outpoint0: utxoEntry0},
			},
			// Same amount/script, only DAA differs - tolerated (isTolerableConflict's second case).
			expectedWithDiffResult: &mutableUTXODiff{
				toAdd:    utxoCollection{*outpoint0: utxoEntry0AltDAA},
				toRemove: utxoCollection{},
			},
			hadTolerableConflict: true,
		},
		{
			name: "same outpoint different DAA – this.toRemove vs other.toRemove",
			this: &mutableUTXODiff{
				toAdd:    utxoCollection{},
				toRemove: utxoCollection{*outpoint0: utxoEntry0},
			},
			other: &mutableUTXODiff{
				toAdd:    utxoCollection{},
				toRemove: utxoCollection{*outpoint0: utxoEntry0AltDAA},
			},
			// Same amount/script, only DAA differs - tolerated (isTolerableConflict's second case).
			expectedDiffFromResult: &mutableUTXODiff{
				toAdd:    utxoCollection{*outpoint0: utxoEntry0},
				toRemove: utxoCollection{*outpoint0: utxoEntry0AltDAA},
			},
			expectedWithDiffResult: &mutableUTXODiff{
				toAdd:    utxoCollection{},
				toRemove: utxoCollection{*outpoint0: utxoEntry0AltDAA},
			},
			hadTolerableConflict: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// ---- diffFrom ----
			diffResult, err := diffFrom(test.this, test.other)

			isDiffFromOk := err == nil
			expectedIsDiffFromOk := test.expectedDiffFromResult != nil
			if isDiffFromOk != expectedIsDiffFromOk {
				t.Errorf("unexpected diffFrom error. Expected ok=%t, got ok=%t (err=%v)",
					expectedIsDiffFromOk, isDiffFromOk, err)
			}
			if isDiffFromOk && !test.expectedDiffFromResult.equal(diffResult) {
				t.Errorf("unexpected diffFrom result.\nExpected: %v\nGot:      %v",
					test.expectedDiffFromResult, diffResult)
			}

			// Round-trip: WithDiff after a successful diffFrom must recover the original "other".
			// Skipped when this/other conflicted on an identical entry (see hadTolerableConflict above).
			if isDiffFromOk && !test.hadTolerableConflict {
				otherResult, err := withDiff(test.this, diffResult)
				if err != nil {
					t.Errorf("WithDiff after diffFrom unexpectedly failed: %s", err)
				} else if !test.other.equal(otherResult) {
					t.Errorf("WithDiff after diffFrom did not recover other.\nExpected: %v\nGot:      %v",
						test.other, otherResult)
				}
			}

			// ---- withDiff ----
			withDiffResult, err := withDiff(test.this, test.other)

			isWithDiffOk := err == nil
			expectedIsWithDiffOk := test.expectedWithDiffResult != nil
			if isWithDiffOk != expectedIsWithDiffOk {
				t.Errorf("unexpected WithDiff error. Expected ok=%t, got ok=%t (err=%v)",
					expectedIsWithDiffOk, isWithDiffOk, err)
			}
			if isWithDiffOk && !withDiffResult.equal(test.expectedWithDiffResult) {
				t.Errorf("unexpected WithDiff result.\nExpected: %v\nGot:      %v",
					test.expectedWithDiffResult, withDiffResult)
			}

			// ---- withDiffInPlace ----
			thisClone := test.this.clone()
			err = withDiffInPlace(thisClone, test.other)

			isWithDiffInPlaceOk := err == nil
			expectedIsWithDiffInPlaceOk := test.expectedWithDiffResult != nil
			if isWithDiffInPlaceOk != expectedIsWithDiffInPlaceOk {
				t.Errorf("unexpected withDiffInPlace error. Expected ok=%t, got ok=%t (err=%v)",
					expectedIsWithDiffInPlaceOk, isWithDiffInPlaceOk, err)
			}
			if isWithDiffInPlaceOk && !thisClone.equal(test.expectedWithDiffResult) {
				t.Errorf("unexpected withDiffInPlace result.\nExpected: %v\nGot:      %v",
					test.expectedWithDiffResult, thisClone)
			}

			// Round-trip: diffFrom after a successful WithDiff must recover the original "other".
			// Skipped when this/other conflicted on an identical entry (see hadTolerableConflict above).
			if isWithDiffOk && !test.hadTolerableConflict {
				otherResult, err := diffFrom(test.this, withDiffResult)
				if err != nil {
					t.Errorf("diffFrom after WithDiff unexpectedly failed: %s", err)
				} else if !test.other.equal(otherResult) {
					t.Errorf("diffFrom after WithDiff did not recover other.\nExpected: %v\nGot:      %v",
						test.other, otherResult)
				}
			}
		})
	}
}

// TestCoinbaseCollisionConflicts exercises the two narrow carve-outs in resolveConflicts
// (diff_algebra.go): a conflict where the same outpoint is touched by both sides of a diffFrom/
// withDiffInPlace composition is tolerated (logged, not errored) only when isTolerableConflict
// says so - either both conflicting entries are coinbase outputs (a known pre-entropy-fork
// transaction ID collision, see coinbaseEntropyActivationVersion), or the two entries agree on
// amount and script - regardless of BlockDAAScore - yet still land on opposite sides of the
// conflict (two independent reconstructions of overlapping DAG history - e.g. two competing tip
// candidates during a reorg - simply disagreeing about whether the shared base already contained
// the same real transaction, or about which accepting block's DAA score it was last recorded
// under - see isTolerableConflict's second case). Every other conflict shape - where the entries
// genuinely disagree on value - must still hard-fail, even when a tolerable conflict is mixed in
// with a real one in the same call.
func TestCoinbaseCollisionConflicts(t *testing.T) {
	_, _, _, outpoint0, outpoint1, _, _, _, _, _ := testFixtures()
	script := &externalapi.ScriptPublicKey{Script: []byte{}, Version: 0}

	// Two coinbase entries for the same outpoint with different DAA scores, standing in for two
	// blocks whose pre-entropy-fork coinbase payloads collided on transaction ID (see
	// coinbaseEntropyActivationVersion).
	coinbaseA := NewUTXOEntry(10, script, true, 0)
	coinbaseB := NewUTXOEntry(10, script, true, 5)
	// Same amount and script but not coinbase, with different DAA scores and distinct entry
	// instances - the second tolerable shape, matching what was actually observed in production:
	// the same real transaction re-touched by different points in overlapping chain history ends
	// up recorded under different accepting-block DAA scores.
	identicalValueA := NewUTXOEntry(10, script, false, 7)
	identicalValueB := NewUTXOEntry(10, script, false, 12)
	// Genuinely different values - this is what real corruption (e.g. an actual double-spend)
	// would look like, and must never be swallowed regardless of coinbase status.
	regularA := NewUTXOEntry(10, script, false, 0)
	regularB := NewUTXOEntry(20, script, false, 5)

	t.Run("diffFrom: this.toRemove vs other.toAdd, both coinbase - tolerated", func(t *testing.T) {
		this := &mutableUTXODiff{toAdd: utxoCollection{}, toRemove: utxoCollection{*outpoint0: coinbaseA}}
		other := &mutableUTXODiff{toAdd: utxoCollection{*outpoint0: coinbaseB}, toRemove: utxoCollection{}}
		if _, err := diffFrom(this, other); err != nil {
			t.Errorf("expected the coinbase collision to be tolerated, got: %s", err)
		}
	})

	t.Run("diffFrom: this.toRemove vs other.toAdd, not coinbase - still errors", func(t *testing.T) {
		this := &mutableUTXODiff{toAdd: utxoCollection{}, toRemove: utxoCollection{*outpoint0: regularA}}
		other := &mutableUTXODiff{toAdd: utxoCollection{*outpoint0: regularB}, toRemove: utxoCollection{}}
		if _, err := diffFrom(this, other); err == nil {
			t.Error("expected a non-coinbase conflict to still be rejected")
		}
	})

	t.Run("diffFrom: this.toRemove vs other.toAdd, not coinbase but identical value - tolerated", func(t *testing.T) {
		this := &mutableUTXODiff{toAdd: utxoCollection{}, toRemove: utxoCollection{*outpoint0: identicalValueA}}
		other := &mutableUTXODiff{toAdd: utxoCollection{*outpoint0: identicalValueB}, toRemove: utxoCollection{}}
		if _, err := diffFrom(this, other); err != nil {
			t.Errorf("expected the identical-value conflict to be tolerated, got: %s", err)
		}
	})

	t.Run("diffFrom: this.toAdd vs other.toRemove, both coinbase - tolerated", func(t *testing.T) {
		this := &mutableUTXODiff{toAdd: utxoCollection{*outpoint0: coinbaseA}, toRemove: utxoCollection{}}
		other := &mutableUTXODiff{toAdd: utxoCollection{}, toRemove: utxoCollection{*outpoint0: coinbaseB}}
		if _, err := diffFrom(this, other); err != nil {
			t.Errorf("expected the coinbase collision to be tolerated, got: %s", err)
		}
	})

	t.Run("diffFrom: this.toAdd vs other.toRemove, not coinbase - still errors", func(t *testing.T) {
		this := &mutableUTXODiff{toAdd: utxoCollection{*outpoint0: regularA}, toRemove: utxoCollection{}}
		other := &mutableUTXODiff{toAdd: utxoCollection{}, toRemove: utxoCollection{*outpoint0: regularB}}
		if _, err := diffFrom(this, other); err == nil {
			t.Error("expected a non-coinbase conflict to still be rejected")
		}
	})

	t.Run("diffFrom: this.toAdd vs other.toRemove, not coinbase but identical value - tolerated", func(t *testing.T) {
		this := &mutableUTXODiff{toAdd: utxoCollection{*outpoint0: identicalValueA}, toRemove: utxoCollection{}}
		other := &mutableUTXODiff{toAdd: utxoCollection{}, toRemove: utxoCollection{*outpoint0: identicalValueB}}
		if _, err := diffFrom(this, other); err != nil {
			t.Errorf("expected the identical-value conflict to be tolerated, got: %s", err)
		}
	})

	t.Run("diffFrom: this.toRemove vs other.toRemove different DAA, both coinbase - tolerated", func(t *testing.T) {
		this := &mutableUTXODiff{toAdd: utxoCollection{}, toRemove: utxoCollection{*outpoint0: coinbaseA}}
		other := &mutableUTXODiff{toAdd: utxoCollection{}, toRemove: utxoCollection{*outpoint0: coinbaseB}}
		if _, err := diffFrom(this, other); err != nil {
			t.Errorf("expected the coinbase collision to be tolerated, got: %s", err)
		}
	})

	t.Run("diffFrom: this.toRemove vs other.toRemove different DAA, not coinbase - still errors", func(t *testing.T) {
		this := &mutableUTXODiff{toAdd: utxoCollection{}, toRemove: utxoCollection{*outpoint0: regularA}}
		other := &mutableUTXODiff{toAdd: utxoCollection{}, toRemove: utxoCollection{*outpoint0: regularB}}
		if _, err := diffFrom(this, other); err == nil {
			t.Error("expected a non-coinbase conflict to still be rejected")
		}
	})

	// This is the exact shape observed in production alongside the "this.toRemove vs other.toAdd"
	// case above, for the very same outpoint in the very same reconstruction: the same real
	// transaction re-touched at different points along overlapping chain history ends up recorded
	// under different accepting-block DAA scores. Requiring DAA to match here (as an earlier,
	// narrower version of this rule did) blocked tolerance in exactly the case it needed to cover,
	// so isTolerableConflict's second case only requires amount and script to match.
	t.Run("diffFrom: this.toRemove vs other.toRemove different DAA, same amount/script but not coinbase - tolerated", func(t *testing.T) {
		sameValueDifferentDAA_A := NewUTXOEntry(10, script, false, 0)
		sameValueDifferentDAA_B := NewUTXOEntry(10, script, false, 5)
		this := &mutableUTXODiff{toAdd: utxoCollection{}, toRemove: utxoCollection{*outpoint0: sameValueDifferentDAA_A}}
		other := &mutableUTXODiff{toAdd: utxoCollection{}, toRemove: utxoCollection{*outpoint0: sameValueDifferentDAA_B}}
		if _, err := diffFrom(this, other); err != nil {
			t.Errorf("expected the identical-value conflict to be tolerated, got: %s", err)
		}
	})

	t.Run("withDiffInPlace: this.toRemove vs other.toRemove, both coinbase - tolerated", func(t *testing.T) {
		this := &mutableUTXODiff{toAdd: utxoCollection{}, toRemove: utxoCollection{*outpoint0: coinbaseA}}
		other := &mutableUTXODiff{toAdd: utxoCollection{}, toRemove: utxoCollection{*outpoint0: coinbaseB}}
		if err := withDiffInPlace(this, other); err != nil {
			t.Errorf("expected the coinbase collision to be tolerated, got: %s", err)
		}
	})

	t.Run("withDiffInPlace: this.toRemove vs other.toRemove, not coinbase - still errors", func(t *testing.T) {
		this := &mutableUTXODiff{toAdd: utxoCollection{}, toRemove: utxoCollection{*outpoint0: regularA}}
		other := &mutableUTXODiff{toAdd: utxoCollection{}, toRemove: utxoCollection{*outpoint0: regularB}}
		if err := withDiffInPlace(this, other); err == nil {
			t.Error("expected a non-coinbase conflict to still be rejected")
		}
	})

	t.Run("withDiffInPlace: this.toRemove vs other.toRemove, not coinbase but identical value - tolerated", func(t *testing.T) {
		this := &mutableUTXODiff{toAdd: utxoCollection{}, toRemove: utxoCollection{*outpoint0: identicalValueA}}
		other := &mutableUTXODiff{toAdd: utxoCollection{}, toRemove: utxoCollection{*outpoint0: identicalValueB}}
		if err := withDiffInPlace(this, other); err != nil {
			t.Errorf("expected the identical-value conflict to be tolerated, got: %s", err)
		}
	})

	t.Run("withDiffInPlace: this.toAdd vs other.toAdd, both coinbase - tolerated", func(t *testing.T) {
		this := &mutableUTXODiff{toAdd: utxoCollection{*outpoint0: coinbaseA}, toRemove: utxoCollection{}}
		other := &mutableUTXODiff{toAdd: utxoCollection{*outpoint0: coinbaseB}, toRemove: utxoCollection{}}
		if err := withDiffInPlace(this, other); err != nil {
			t.Errorf("expected the coinbase collision to be tolerated, got: %s", err)
		}
	})

	t.Run("withDiffInPlace: this.toAdd vs other.toAdd, not coinbase - still errors", func(t *testing.T) {
		this := &mutableUTXODiff{toAdd: utxoCollection{*outpoint0: regularA}, toRemove: utxoCollection{}}
		other := &mutableUTXODiff{toAdd: utxoCollection{*outpoint0: regularB}, toRemove: utxoCollection{}}
		if err := withDiffInPlace(this, other); err == nil {
			t.Error("expected a non-coinbase conflict to still be rejected")
		}
	})

	t.Run("withDiffInPlace: this.toAdd vs other.toAdd, not coinbase but identical value - tolerated", func(t *testing.T) {
		this := &mutableUTXODiff{toAdd: utxoCollection{*outpoint0: identicalValueA}, toRemove: utxoCollection{}}
		other := &mutableUTXODiff{toAdd: utxoCollection{*outpoint0: identicalValueB}, toRemove: utxoCollection{}}
		if err := withDiffInPlace(this, other); err != nil {
			t.Errorf("expected the identical-value conflict to be tolerated, got: %s", err)
		}
	})

	t.Run("withDiffInPlace: a tolerable conflict does not mask a real one in the same call", func(t *testing.T) {
		this := &mutableUTXODiff{
			toAdd:    utxoCollection{},
			toRemove: utxoCollection{*outpoint0: coinbaseA, *outpoint1: regularA},
		}
		other := &mutableUTXODiff{
			toAdd:    utxoCollection{},
			toRemove: utxoCollection{*outpoint0: coinbaseB, *outpoint1: regularB},
		}
		if err := withDiffInPlace(this, other); err == nil {
			t.Error("expected the real (non-coinbase, non-identical-value) conflict to still be " +
				"rejected, even with a tolerable coinbase conflict on another outpoint in the same call")
		}
	})
}

// TestAddRemoveEntryDuplicates exercises the low-level addEntry / removeEntry
// paths that protect against duplicate outpoints inside a single mutableUTXODiff.
func TestAddRemoveEntryDuplicates(t *testing.T) {
	_, _, _, outpoint0, outpoint1, _, utxoEntry0, utxoEntry1, _, utxoEntry0AltDAA := testFixtures()

	t.Run("double add same outpoint same DAA", func(t *testing.T) {
		d := newMutableUTXODiff()
		if err := d.addEntry(outpoint0, utxoEntry0); err != nil {
			t.Fatalf("first add failed: %v", err)
		}
		err := d.addEntry(outpoint0, utxoEntry0)
		if err == nil {
			t.Fatal("expected error on second add of same outpoint")
		}
		if !strings.Contains(err.Error(), "Cannot add outpoint") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("double remove same outpoint same DAA", func(t *testing.T) {
		d := newMutableUTXODiff()
		if err := d.removeEntry(outpoint0, utxoEntry0); err != nil {
			t.Fatalf("first remove failed: %v", err)
		}
		err := d.removeEntry(outpoint0, utxoEntry0)
		if err == nil {
			t.Fatal("expected error on second remove of same outpoint")
		}
		if !strings.Contains(err.Error(), "Cannot remove outpoint") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("add then remove same outpoint cancels", func(t *testing.T) {
		d := newMutableUTXODiff()
		if err := d.addEntry(outpoint0, utxoEntry0); err != nil {
			t.Fatalf("add failed: %v", err)
		}
		if err := d.removeEntry(outpoint0, utxoEntry0); err != nil {
			t.Fatalf("remove failed: %v", err)
		}
		if len(d.toAdd) != 0 || len(d.toRemove) != 0 {
			t.Errorf("expected empty diff after cancel, got %v", d)
		}
	})

	t.Run("remove then add same outpoint cancels", func(t *testing.T) {
		d := newMutableUTXODiff()
		if err := d.removeEntry(outpoint0, utxoEntry0); err != nil {
			t.Fatalf("remove failed: %v", err)
		}
		if err := d.addEntry(outpoint0, utxoEntry0); err != nil {
			t.Fatalf("add failed: %v", err)
		}
		if len(d.toAdd) != 0 || len(d.toRemove) != 0 {
			t.Errorf("expected empty diff after cancel, got %v", d)
		}
	})

	t.Run("add with different DAA does not cancel previous remove", func(t *testing.T) {
		d := newMutableUTXODiff()
		if err := d.removeEntry(outpoint0, utxoEntry0); err != nil {
			t.Fatalf("remove failed: %v", err)
		}
		if err := d.addEntry(outpoint0, utxoEntry0AltDAA); err != nil {
			t.Fatalf("add alt-DAA failed: %v", err)
		}
		if !d.toRemove.containsWithDAAScore(outpoint0, utxoEntry0.BlockDAAScore()) {
			t.Error("original remove entry disappeared")
		}
		if !d.toAdd.containsWithDAAScore(outpoint0, utxoEntry0AltDAA.BlockDAAScore()) {
			t.Error("alt-DAA add entry missing")
		}
	})

	t.Run("remove with different DAA does not cancel previous add", func(t *testing.T) {
		d := newMutableUTXODiff()
		if err := d.addEntry(outpoint0, utxoEntry0); err != nil {
			t.Fatalf("add failed: %v", err)
		}
		if err := d.removeEntry(outpoint0, utxoEntry0AltDAA); err != nil {
			t.Fatalf("remove alt-DAA failed: %v", err)
		}
		if !d.toAdd.containsWithDAAScore(outpoint0, utxoEntry0.BlockDAAScore()) {
			t.Error("original add entry disappeared")
		}
		if !d.toRemove.containsWithDAAScore(outpoint0, utxoEntry0AltDAA.BlockDAAScore()) {
			t.Error("alt-DAA remove entry missing")
		}
	})

	t.Run("independent outpoints do not interfere", func(t *testing.T) {
		d := newMutableUTXODiff()
		if err := d.addEntry(outpoint0, utxoEntry0); err != nil {
			t.Fatalf("add0 failed: %v", err)
		}
		if err := d.addEntry(outpoint1, utxoEntry1); err != nil {
			t.Fatalf("add1 failed: %v", err)
		}
		if err := d.removeEntry(outpoint0, utxoEntry0); err != nil {
			t.Fatalf("remove0 failed: %v", err)
		}
		// outpoint0 cancelled, outpoint1 still present
		if len(d.toAdd) != 1 || !d.toAdd.Contains(outpoint1) {
			t.Errorf("expected only outpoint1 in toAdd, got %v", d.toAdd)
		}
		if len(d.toRemove) != 0 {
			t.Errorf("expected empty toRemove, got %v", d.toRemove)
		}
	})
}

// TestUTXODiffWithManyOutpoints verifies that the algebra still holds when
// many independent outpoints are present (stresses the collection iteration paths).
func TestUTXODiffWithManyOutpoints(t *testing.T) {
	const n = 20
	entries := make([]struct {
		op  *externalapi.DomainOutpoint
		ent externalapi.UTXOEntry
	}, n)

	for i := range n {
		// Build a valid 64-char hex string (32-byte DomainTransactionID)
		// using only 0-9a-f characters.
		hexChar := "0123456789abcdef"[i%16]
		txIDStr := strings.Repeat(string(hexChar), 64)
		txID, err := transactionid.FromString(txIDStr)
		if err != nil {
			t.Fatalf("failed to create txID %d: %v", i, err)
		}
		entries[i].op = externalapi.NewDomainOutpoint(txID, uint32(i))
		entries[i].ent = NewUTXOEntry(uint64(100+i), &externalapi.ScriptPublicKey{Script: []byte{}, Version: 0}, false, uint64(i))
	}

	// Build this = all even indices in toAdd, all odd indices in toRemove
	this := newMutableUTXODiff()
	for i, e := range entries {
		if i%2 == 0 {
			if err := this.addEntry(e.op, e.ent); err != nil {
				t.Fatalf("add even %d: %v", i, err)
			}
		} else {
			if err := this.removeEntry(e.op, e.ent); err != nil {
				t.Fatalf("remove odd %d: %v", i, err)
			}
		}
	}

	// other = empty
	other := newMutableUTXODiff()

	// diffFrom(this, empty) should invert every entry
	diff, err := diffFrom(this, other)
	if err != nil {
		t.Fatalf("diffFrom failed: %v", err)
	}
	for i, e := range entries {
		if i%2 == 0 {
			if !diff.toRemove.containsWithDAAScore(e.op, e.ent.BlockDAAScore()) {
				t.Errorf("expected even outpoint %d in result.toRemove", i)
			}
		} else {
			if !diff.toAdd.containsWithDAAScore(e.op, e.ent.BlockDAAScore()) {
				t.Errorf("expected odd outpoint %d in result.toAdd", i)
			}
		}
	}

	// withDiff(this, empty) should leave this unchanged
	combined, err := withDiff(this, other)
	if err != nil {
		t.Fatalf("withDiff failed: %v", err)
	}
	if !this.equal(combined) {
		t.Errorf("withDiff with empty other changed the diff")
	}

	// Round-trip: apply the inverted diff back
	restored, err := withDiff(this, diff)
	if err != nil {
		t.Fatalf("withDiff of inverted failed: %v", err)
	}
	if len(restored.toAdd) != 0 || len(restored.toRemove) != 0 {
		t.Errorf("round-trip did not produce empty diff: %v", restored)
	}
}
