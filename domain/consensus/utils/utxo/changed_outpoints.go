package utxo

import (
	"weak"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/pkg/errors"
)

// CloneMutableRecordingChanges clones diff like CloneMutable, and makes the clone record every
// outpoint it mutates from then on, so DiffFromChanged can later diff it against diff by looking at
// those outpoints alone.
func CloneMutableRecordingChanges(diff externalapi.UTXODiff) (externalapi.MutableUTXODiff, error) {
	immutable, ok := diff.(*immutableUTXODiff)
	if !ok {
		return nil, errors.New("diff is not of type *immutableUTXODiff")
	}
	if immutable.isInvalidated {
		return nil, errors.New("attempt to clone an invalidated UTXODiff")
	}
	clone := immutable.cloneMutable()
	clone.changed = make(map[externalapi.DomainOutpoint]struct{})
	clone.changedFrom = weak.Make(immutable.mutableUTXODiff)
	return clone, nil
}

// DiffFromChanged returns this.DiffFrom(other) for an other that CloneMutableRecordingChanges cloned
// from this and that was then mutated, by computing diffFrom over only the outpoints other recorded
// as changed. ok is false, and nothing is computed, when other does not record its changes or was
// not cloned from this; the caller then needs DiffFrom. this cannot have changed since the clone:
// mutating a diff invalidates its immutable references, which is an error here.
//
// diffFrom decides each outpoint from that outpoint's four entries alone - this.toAdd, this.toRemove,
// other.toAdd, other.toRemove - and an outpoint whose entries are the same in this and other
// contributes nothing to the result. Every outpoint other has not mutated is such an outpoint, so
// leaving them out does not change the result; it only stops the cost from depending on the size of
// the diffs. That matters during virtual resolution, where both are diffs from virtual: a chunk
// resolves many chain blocks before virtual moves, so the k-th block's selected-parent past already
// holds the changes of the k-1 blocks before it, and a full diffFrom made a chunk cost quadratic in
// its length times its transactions per block.
func DiffFromChanged(this, other externalapi.UTXODiff) (result externalapi.UTXODiff, ok bool, err error) {
	thisImmutable, isImmutable := this.(*immutableUTXODiff)
	if !isImmutable {
		return nil, false, errors.New("this is not of type *immutableUTXODiff")
	}
	otherImmutable, isImmutable := other.(*immutableUTXODiff)
	if !isImmutable {
		return nil, false, errors.New("other is not of type *immutableUTXODiff")
	}
	if thisImmutable.isInvalidated || otherImmutable.isInvalidated {
		return nil, false, errors.New("attempt to read from an invalidated UTXODiff")
	}
	changed := otherImmutable.mutableUTXODiff.changed
	if changed == nil || otherImmutable.mutableUTXODiff.changedFrom != weak.Make(thisImmutable.mutableUTXODiff) {
		return nil, false, nil
	}

	resultMutable, err := diffFrom(restrictToOutpoints(thisImmutable.mutableUTXODiff, changed),
		restrictToOutpoints(otherImmutable.mutableUTXODiff, changed))
	if err != nil {
		return nil, true, err
	}
	return resultMutable.ToImmutable(), true, nil
}

// restrictToOutpoints returns a diff holding only diff's entries at outpoints.
func restrictToOutpoints(diff *mutableUTXODiff, outpoints map[externalapi.DomainOutpoint]struct{}) *mutableUTXODiff {
	restricted := &mutableUTXODiff{
		toAdd:    make(utxoCollection, min(len(outpoints), len(diff.toAdd))),
		toRemove: make(utxoCollection, min(len(outpoints), len(diff.toRemove))),
	}
	for outpoint := range outpoints {
		if entry, ok := diff.toAdd.Get(&outpoint); ok {
			restricted.toAdd.add(&outpoint, entry)
		}
		if entry, ok := diff.toRemove.Get(&outpoint); ok {
			restricted.toRemove.add(&outpoint, entry)
		}
	}
	return restricted
}
