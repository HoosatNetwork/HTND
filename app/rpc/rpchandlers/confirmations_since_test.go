package rpchandlers

import "testing"

// TestConfirmationsSinceDoesNotWrap pins how GetTransactionStatus counts confirmations. The count was the selected
// parent's blue score minus the containing block's plus one, in unsigned arithmetic, so a transaction whose block had
// a blue score above the selected parent's - an unmerged tip, reported as pending - came back with about 2^64
// confirmations.
func TestConfirmationsSinceDoesNotWrap(t *testing.T) {
	tests := []struct {
		name                  string
		selectedParent, block uint64
		expectedConfirmations uint64
	}{
		{"the selected parent itself", 100, 100, 1},
		{"a block in the selected parent's past", 100, 40, 61},
		{"the lowest blue score", 100, 0, 101},
		{"one above the selected parent", 100, 101, 0},
		{"well above the selected parent", 100, 150, 0},
	}
	for _, test := range tests {
		if got := confirmationsSince(test.selectedParent, test.block); got != test.expectedConfirmations {
			t.Errorf("%s: selected parent blue score %d, block blue score %d: got %d confirmations, want %d",
				test.name, test.selectedParent, test.block, got, test.expectedConfirmations)
		}
	}
}
