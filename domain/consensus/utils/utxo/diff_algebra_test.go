package utxo

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/transactionid"

	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
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
	_, _, _, outpoint0, outpoint1, outpoint2, utxoEntry0, utxoEntry1, utxoEntry2, utxoEntry0AltDAA := testFixtures()
	// Keep the original single-outpoint names used by the classic table
	utxoEntry1 = NewUTXOEntry(10, &externalapi.ScriptPublicKey{Script: []byte{}, Version: 0}, true, 0)
	utxoEntry2 = NewUTXOEntry(20, &externalapi.ScriptPublicKey{Script: []byte{}, Version: 0}, true, 1)

	// Classic single-outpoint table (identical to original)
	tests := []struct {
		name                   string
		this                   *mutableUTXODiff
		other                  *mutableUTXODiff
		expectedDiffFromResult *mutableUTXODiff
		expectedWithDiffResult *mutableUTXODiff
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
			expectedWithDiffResult: nil,
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
			expectedDiffFromResult: nil,
			expectedWithDiffResult: &mutableUTXODiff{
				toAdd:    utxoCollection{},
				toRemove: utxoCollection{},
			},
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
			expectedWithDiffResult: nil,
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
			expectedDiffFromResult: nil,
			expectedWithDiffResult: &mutableUTXODiff{
				toAdd:    utxoCollection{*outpoint0: utxoEntry2},
				toRemove: utxoCollection{},
			},
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
			expectedDiffFromResult: nil,
			expectedWithDiffResult: &mutableUTXODiff{
				toAdd:    utxoCollection{},
				toRemove: utxoCollection{},
			},
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
			expectedWithDiffResult: nil,
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
			expectedWithDiffResult: nil,
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
			expectedWithDiffResult: nil,
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
			expectedDiffFromResult: nil,
			expectedWithDiffResult: nil,
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
			expectedWithDiffResult: nil,
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
			expectedWithDiffResult: nil,
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
			expectedDiffFromResult: nil, // conflict on outpoint0 (this.toAdd vs other.toRemove)
			expectedWithDiffResult: &mutableUTXODiff{
				toAdd:    utxoCollection{*outpoint1: utxoEntry1},
				toRemove: utxoCollection{},
			},
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
			expectedDiffFromResult: nil, // this.toRemove vs other.toAdd on outpoint1
			expectedWithDiffResult: &mutableUTXODiff{
				toAdd:    utxoCollection{*outpoint0: utxoEntry0, *outpoint2: utxoEntry2},
				toRemove: utxoCollection{},
			},
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
			expectedWithDiffResult: nil, // duplicate toAdd / toRemove
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
			expectedWithDiffResult: nil, // cannot have two different entries for same outpoint in toAdd
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
			expectedDiffFromResult: nil, // different DAA scores in toRemove is an error
			expectedWithDiffResult: nil,
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

			// Round-trip: WithDiff after a successful diffFrom must recover the original "other"
			if isDiffFromOk {
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

			// Round-trip: diffFrom after a successful WithDiff must recover the original "other"
			if isWithDiffOk {
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

	for i := 0; i < n; i++ {
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
