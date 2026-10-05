package consensus

import (
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
)

func TestDisqualificationStreakReportsOnceAtTheThreshold(t *testing.T) {
	streak := disqualificationStreak{threshold: 3}

	for i := 1; i <= 2; i++ {
		if streak.note(externalapi.StatusDisqualifiedFromChain) {
			t.Fatalf("reported after %d disqualified blocks, threshold is 3", i)
		}
	}
	// A block still pending verification says nothing either way.
	if streak.note(externalapi.StatusUTXOPendingVerification) {
		t.Fatal("a pending block must not report")
	}
	if !streak.note(externalapi.StatusDisqualifiedFromChain) {
		t.Fatal("the third disqualified block in a row must report")
	}
	if streak.note(externalapi.StatusDisqualifiedFromChain) {
		t.Fatal("the streak must report once, not on every block after the threshold")
	}

	// A valid block ends the streak, and a new one has to build up from zero.
	streak.note(externalapi.StatusUTXOValid)
	for i := 1; i <= 2; i++ {
		if streak.note(externalapi.StatusDisqualifiedFromChain) {
			t.Fatalf("reported %d blocks into a new streak", i)
		}
	}
	if !streak.note(externalapi.StatusDisqualifiedFromChain) {
		t.Fatal("a new streak reaching the threshold must report again")
	}
}

func TestDisqualificationStreakDisabledAtZero(t *testing.T) {
	streak := disqualificationStreak{}
	for i := 0; i < 100; i++ {
		if streak.note(externalapi.StatusDisqualifiedFromChain) {
			t.Fatal("a zero threshold disables the check")
		}
	}
}
