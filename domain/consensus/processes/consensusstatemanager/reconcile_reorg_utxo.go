package consensusstatemanager

import (
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/utxo"
)

// reconcileWinningBranchUTXO returns a copy of losingBranch where every outpoint that exists on the
// same side (both toAdd or both toRemove) of both losingBranch and winningBranch, with matching
// amount and script but a different BlockDAAScore, has its entry replaced by winningBranch's own
// entry.
//
// This exists because DiffFrom/WithDiffInPlace (domain/consensus/utils/utxo/diff_algebra.go) are
// keyed on (outpoint, BlockDAAScore), not outpoint alone. Two UTXODiffs built by independently
// walking competing chain branches - which is exactly what happens here, comparing the new
// candidate selected tip against the tip it's replacing (or vice versa) - can legitimately accept
// the same transaction at different points in each branch's own reconstruction, stamping it with
// each branch's own BlockDAAScore (see calculatePastUTXOAndAcceptanceDataWithSelectedParentUTXO).
// When that composed/conflicting outpoint later reaches DiffFrom, the algebra has no principled way
// to decide which BlockDAAScore is canonical - and whichever way it guesses, replaying the result
// later via WithDiffInPlace can't correctly cancel a same-outpoint entry against a different
// BlockDAAScore, so the entry ends up duplicated (present in both toAdd and toRemove, or bucketed
// unconditionally into one side) instead of resolved. That corruption doesn't fail the block that
// caused it - it fails an unrelated, later block that happens to reconstruct its past UTXO by
// walking through the poisoned diff-chain segment, showing up as ErrBadUTXOCommitment or
// ErrMissingTxOut far from the actual cause.
//
// Reconciling here, before the two branches are ever diffed against each other, means DiffFrom sees
// byte-identical entries for these outpoints and doesn't need to resolve anything: there's no
// disagreement left to tolerate, bucket, or fail to replay correctly. A same-outpoint mismatch that
// ISN'T just a BlockDAAScore disagreement (different amount/script, or present as toAdd on one side
// and toRemove on the other) is left untouched - that's a genuine conflict, not a bookkeeping
// artifact, and should still surface through the existing DiffFrom/isTolerableConflict path.
func reconcileWinningBranchUTXO(winningBranch, losingBranch externalapi.UTXODiff) (externalapi.UTXODiff, error) {
	newToAdd, addChanged, err := reconcileCollectionToWinner(winningBranch.ToAdd(), losingBranch.ToAdd())
	if err != nil {
		return nil, err
	}
	newToRemove, removeChanged, err := reconcileCollectionToWinner(winningBranch.ToRemove(), losingBranch.ToRemove())
	if err != nil {
		return nil, err
	}
	if !addChanged && !removeChanged {
		return losingBranch, nil
	}
	return utxo.NewUTXODiffFromCollections(newToAdd, newToRemove)
}

// reconcileCollectionToWinner returns a copy of losing with every entry that only disagrees with
// winning on BlockDAAScore (same outpoint, same amount, same script) replaced by winning's entry,
// and whether any such replacement was made.
func reconcileCollectionToWinner(winning, losing externalapi.UTXOCollection) (externalapi.UTXOCollection, bool, error) {
	changed := false
	merged := make(map[externalapi.DomainOutpoint]externalapi.UTXOEntry, losing.Len())

	iterator := losing.Iterator()
	defer iterator.Close()
	for ok := iterator.First(); ok; ok = iterator.Next() {
		outpoint, losingEntry, err := iterator.Get()
		if err != nil {
			return nil, false, err
		}

		if winningEntry, ok := winning.Get(outpoint); ok &&
			winningEntry.BlockDAAScore() != losingEntry.BlockDAAScore() &&
			winningEntry.Amount() == losingEntry.Amount() &&
			winningEntry.ScriptPublicKey().Equal(losingEntry.ScriptPublicKey()) {
			merged[*outpoint] = winningEntry
			changed = true
			continue
		}

		merged[*outpoint] = losingEntry
	}

	return utxo.NewUTXOCollection(merged), changed, nil
}
