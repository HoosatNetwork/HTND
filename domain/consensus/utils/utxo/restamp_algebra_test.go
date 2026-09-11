package utxo_test

import (
	"fmt"
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/utxo"
)

// One outpoint, and the only thing that varies is which merging block's DAA score a view stamps it
// with - or whether the view holds it at all. -1 means absent.
var restampStates = []int{-1, 0, 1, 2}

var restampOutpoint = externalapi.DomainOutpoint{
	TransactionID: externalapi.DomainTransactionID(*externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{7})),
	Index:         0,
}

func restampEntry(stamp int) externalapi.UTXOEntry {
	return utxo.NewUTXOEntry(1000, &externalapi.ScriptPublicKey{Script: []byte{0x51}}, false, uint64(stamp))
}

// canonicalDiff is the diff that takes a view holding `from` to a view holding `to`.
func canonicalDiff(t *testing.T, from, to int) externalapi.UTXODiff {
	toAdd := map[externalapi.DomainOutpoint]externalapi.UTXOEntry{}
	toRemove := map[externalapi.DomainOutpoint]externalapi.UTXOEntry{}
	if from != to {
		if from >= 0 {
			toRemove[restampOutpoint] = restampEntry(from)
		}
		if to >= 0 {
			toAdd[restampOutpoint] = restampEntry(to)
		}
	}
	diff, err := utxo.NewUTXODiffFromCollections(utxo.NewUTXOCollection(toAdd), utxo.NewUTXOCollection(toRemove))
	if err != nil {
		t.Fatalf("NewUTXODiffFromCollections: %+v", err)
	}
	return diff
}

// apply returns the state a view reaches from `state` under diff, or an explanation if the diff
// cannot apply to that state.
func apply(state int, diff externalapi.UTXODiff) (int, string) {
	if removed, ok := diff.ToRemove().Get(&restampOutpoint); ok {
		if state != int(removed.BlockDAAScore()) {
			return 0, fmt.Sprintf("removes stamp %d from a view holding %d", removed.BlockDAAScore(), state)
		}
		state = -1
	}
	if added, ok := diff.ToAdd().Get(&restampOutpoint); ok {
		if state != -1 {
			return 0, fmt.Sprintf("adds stamp %d to a view already holding %d", added.BlockDAAScore(), state)
		}
		state = int(added.BlockDAAScore())
	}
	return state, ""
}

func describeDiff(diff externalapi.UTXODiff) string {
	add, remove := "-", "-"
	if e, ok := diff.ToAdd().Get(&restampOutpoint); ok {
		add = fmt.Sprint(e.BlockDAAScore())
	}
	if e, ok := diff.ToRemove().Get(&restampOutpoint); ok {
		remove = fmt.Sprint(e.BlockDAAScore())
	}
	return fmt.Sprintf("{add:%s remove:%s}", add, remove)
}

// TestUTXODiffAlgebraKeepsRestamps checks DiffFrom, WithDiff and Reversed on every placement of one
// coin across a base view and two derived views, including the case that matters most: the same
// coin present in two views with different DAA stamps. A coin's stamp is the DAA score of the block
// that merged its transaction, so two competing branches routinely hold the same coin at different
// stamps, and the diff between them has to say so - as a removal of one stamp and an addition of the
// other.
//
// reconcileWinningBranchUTXO existed because this was believed not to work. It does, and erasing
// the difference before diffing was what corrupted the losing tip's stored diff at every tip switch
// where the branches disagreed on a stamp. This test is the record that the algebra needs no help.
func TestUTXODiffAlgebraKeepsRestamps(t *testing.T) {
	failures := 0
	report := func(format string, args ...any) {
		failures++
		if failures <= 40 {
			t.Logf(format, args...)
		}
	}
	for _, base := range restampStates {
		for _, a := range restampStates {
			for _, c := range restampStates {
				dA, dB := canonicalDiff(t, base, a), canonicalDiff(t, base, c)

				// DiffFrom: dA.WithDiff(dA.DiffFrom(dB)) must describe the same view as dB.
				r, err := dA.DiffFrom(dB)
				if err != nil {
					report("DiffFrom   base=%2d a=%2d c=%2d: dA=%s dB=%s -> error %v", base, a, c, describeDiff(dA), describeDiff(dB), err)
				} else if got, problem := apply(a, r); problem != "" || got != c {
					report("DiffFrom   base=%2d a=%2d c=%2d: dA=%s dB=%s -> r=%s takes %d to %d (want %d) %s",
						base, a, c, describeDiff(dA), describeDiff(dB), describeDiff(r), a, got, c, problem)
				}

				// WithDiff: composing base->a with a->c must describe base->c.
				w, err := dA.WithDiff(canonicalDiff(t, a, c))
				if err != nil {
					report("WithDiff   base=%2d a=%2d c=%2d: dA=%s then %s -> error %v", base, a, c, describeDiff(dA), describeDiff(canonicalDiff(t, a, c)), err)
				} else if got, problem := apply(base, w); problem != "" || got != c {
					report("WithDiff   base=%2d a=%2d c=%2d: dA=%s then %s -> %s takes %d to %d (want %d) %s",
						base, a, c, describeDiff(dA), describeDiff(canonicalDiff(t, a, c)), describeDiff(w), base, got, c, problem)
				}
			}
			// Reversed: undoing base->a must land back on base.
			if got, problem := apply(a, canonicalDiff(t, base, a).Reversed()); problem != "" || got != base {
				report("Reversed   base=%2d a=%2d: takes %d to %d %s", base, a, a, got, problem)
			}
		}
	}
	if failures > 0 {
		t.Fatalf("%d algebra case(s) lose or corrupt a restamp", failures)
	}
}
