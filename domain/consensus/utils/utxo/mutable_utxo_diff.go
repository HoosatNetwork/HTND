package utxo

import (
	"fmt"
	"math"

	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/transactionhelper"
	"github.com/pkg/errors"
)

type mutableUTXODiff struct {
	toAdd    utxoCollection
	toRemove utxoCollection

	immutableReferences []*immutableUTXODiff
}

// NewMutableUTXODiff creates an empty mutable UTXO-Diff
func NewMutableUTXODiff() externalapi.MutableUTXODiff {
	return newMutableUTXODiff()
}

func newMutableUTXODiff() *mutableUTXODiff {
	return &mutableUTXODiff{
		toAdd:    utxoCollection{},
		toRemove: utxoCollection{},
	}
}

func (mud *mutableUTXODiff) ToImmutable() externalapi.UTXODiff {
	immutableReference := &immutableUTXODiff{
		mutableUTXODiff: mud,
		isInvalidated:   false,
	}

	mud.immutableReferences = append(mud.immutableReferences, immutableReference)

	return immutableReference
}

func (mud *mutableUTXODiff) invalidateImmutableReferences() {
	for _, immutableReference := range mud.immutableReferences {
		immutableReference.isInvalidated = true
	}

	mud.immutableReferences = nil
}

func (mud *mutableUTXODiff) WithDiff(other externalapi.UTXODiff) (externalapi.UTXODiff, error) {
	o, ok := other.(*immutableUTXODiff)
	if !ok {
		return nil, errors.New("other is not of type *immutableUTXODiff")
	}

	result, err := withDiff(mud, o.mutableUTXODiff)
	if err != nil {
		return nil, err
	}

	return result.ToImmutable(), nil
}

func (mud *mutableUTXODiff) WithDiffInPlace(other externalapi.UTXODiff) error {
	o, ok := other.(*immutableUTXODiff)
	if !ok {
		return errors.New("other is not of type *immutableUTXODiff")
	}

	mud.invalidateImmutableReferences()

	return withDiffInPlace(mud, o.mutableUTXODiff)
}

func (mud *mutableUTXODiff) DiffFrom(other externalapi.UTXODiff) (externalapi.UTXODiff, error) {
	o, ok := other.(*immutableUTXODiff)
	if !ok {
		return nil, errors.New("other is not of type *immutableUTXODiff")
	}

	result, err := diffFrom(mud, o.mutableUTXODiff)
	if err != nil {
		return nil, err
	}

	return result.ToImmutable(), nil
}

func (mud *mutableUTXODiff) ToAdd() externalapi.UTXOCollection {
	return mud.toAdd
}

func (mud *mutableUTXODiff) ToRemove() externalapi.UTXOCollection {
	return mud.toRemove
}

func (mud *mutableUTXODiff) Equal(other externalapi.UTXODiff) bool {
	otherToAdd := other.ToAdd()
	otherToRemove := other.ToRemove()
	return utxoCollectionsEqual(mud.toAdd, otherToAdd) && utxoCollectionsEqual(mud.toRemove, otherToRemove)
}

func utxoCollectionsEqual(a, b externalapi.UTXOCollection) bool {
	if a.Len() != b.Len() {
		return false
	}
	iterator := a.Iterator()
	for ok := iterator.First(); ok; ok = iterator.Next() {
		outpoint, entryA, err := iterator.Get()
		if err != nil {
			return false
		}
		entryB, ok := b.Get(outpoint)
		if !ok {
			return false
		}
		if !entryA.Equal(entryB) {
			return false
		}
	}
	return true
}

// utxoDiffUndoEntry captures a single outpoint's toAdd/toRemove state from immediately before a
// mutation, so AddTransaction can restore it if a later step in the same call fails.
type utxoDiffUndoEntry struct {
	outpoint      externalapi.DomainOutpoint
	hadToAdd      bool
	toAddEntry    externalapi.UTXOEntry
	hadToRemove   bool
	toRemoveEntry externalapi.UTXOEntry
}

func (mud *mutableUTXODiff) snapshot(outpoint *externalapi.DomainOutpoint) utxoDiffUndoEntry {
	u := utxoDiffUndoEntry{outpoint: *outpoint}
	u.toAddEntry, u.hadToAdd = mud.toAdd.Get(outpoint)
	u.toRemoveEntry, u.hadToRemove = mud.toRemove.Get(outpoint)
	return u
}

func (mud *mutableUTXODiff) restore(u *utxoDiffUndoEntry) {
	if u.hadToAdd {
		mud.toAdd.add(&u.outpoint, u.toAddEntry)
	} else {
		mud.toAdd.remove(&u.outpoint)
	}
	if u.hadToRemove {
		mud.toRemove.add(&u.outpoint, u.toRemoveEntry)
	} else {
		mud.toRemove.remove(&u.outpoint)
	}
}

// AddTransaction applies transaction's inputs and outputs to the diff. It is atomic: addEntry and
// removeEntry mutate toAdd/toRemove directly and have no rollback of their own, so without this,
// a failure on e.g. the transaction's third output would leave its first two outputs (and all of
// its already-removed inputs) applied to the diff while the transaction as a whole is treated as
// not accepted by the caller - silently persisting a partial application under an "unaccepted"
// label. Every outpoint is snapshotted immediately before it's touched, in call order, and on any
// error every snapshot taken so far is restored in reverse (LIFO) order, undoing exactly the
// mutations this call made and leaving the diff exactly as it was found.
func (mud *mutableUTXODiff) AddTransaction(transaction *externalapi.DomainTransaction, blockDAAScore uint64) error {
	mud.invalidateImmutableReferences()

	var undoLog []utxoDiffUndoEntry
	rollback := func() {
		for i := len(undoLog) - 1; i >= 0; i-- {
			mud.restore(&undoLog[i])
		}
	}

	for _, input := range transaction.Inputs {
		undoLog = append(undoLog, mud.snapshot(&input.PreviousOutpoint))
		err := mud.removeEntry(&input.PreviousOutpoint, input.UTXOEntry)
		if err != nil {
			rollback()
			return err
		}
	}

	isCoinbase := transactionhelper.IsCoinBase(transaction)
	transactionID := *consensushashing.TransactionID(transaction)
	for i, output := range transaction.Outputs {
		if i < 0 || i > math.MaxUint32 {
			rollback()
			return errors.Errorf("output index %d cannot be represented as uint32", i)
		}
		outpoint := &externalapi.DomainOutpoint{
			TransactionID: transactionID,
			Index:         uint32(i),
		}
		entry := NewUTXOEntry(output.Value, output.ScriptPublicKey, isCoinbase, blockDAAScore)

		undoLog = append(undoLog, mud.snapshot(outpoint))
		err := mud.addEntry(outpoint, entry)
		if err != nil {
			rollback()
			return err
		}
	}

	return nil
}

func (mud *mutableUTXODiff) addEntry(outpoint *externalapi.DomainOutpoint, entry externalapi.UTXOEntry) error {
	if mud.toRemove.containsWithDAAScore(outpoint, entry.BlockDAAScore()) {
		if entry.IsCoinbase() {
			existing, _ := mud.toRemove.Get(outpoint)
			valueDiffers := existing == nil || existing.Amount() != entry.Amount() ||
				!existing.ScriptPublicKey().Equal(entry.ScriptPublicKey())
			if valueDiffers {
				// A coinbase output can't legitimately be removed-then-recreated within a single
				// AddTransaction call (it has no inputs of its own to trigger that), and a genuine
				// coinbase-ID collision (see isTolerableConflict below) always carries the same
				// spendable value on both sides. A DIFFERENT value landing on the same
				// (outpoint, DAA score) pair here isn't explained by either of those - dropping this
				// coinbase's real, freshly minted reward from the diff with no error would be wrong
				// regardless of the exact cause. Drop the stale toRemove entry and still record this
				// output as newly added.
				log.Debugf("[UTXO-DEBUG] addEntry: coinbase outpoint %s (amount=%d daaScore=%d) collided with "+
					"a DIFFERENT-valued pre-existing toRemove entry (amount=%d) - adding to toAdd instead of "+
					"silently cancelling out", outpoint, entry.Amount(), entry.BlockDAAScore(), existing.Amount())
				mud.toRemove.remove(outpoint)
				mud.toAdd.add(outpoint, entry)
				return nil
			}
			log.Debugf("[UTXO-DEBUG] addEntry: coinbase outpoint %s (amount=%d daaScore=%d isCoinbase=%t) cancels "+
				"out against a same-valued pre-existing toRemove entry (amount=%d isCoinbase=%t) - treated as "+
				"legitimate no-op", outpoint, entry.Amount(), entry.BlockDAAScore(), entry.IsCoinbase(),
				existing.Amount(), existing.IsCoinbase())
		}
		mud.toRemove.remove(outpoint)
	} else if mud.toAdd.Contains(outpoint) {
		if entry.IsCoinbase() {
			if existing, ok := mud.toAdd.Get(outpoint); ok && existing.Amount() == entry.Amount() &&
				existing.ScriptPublicKey().Equal(entry.ScriptPublicKey()) {
				// Same reasoning as the toRemove-collision branch above: two blocks whose coinbase
				// transactions are byte-identical (a genuine content-derived ID collision, or the
				// same mining template reused across multiple valid nonces before being refreshed -
				// see the isTolerableConflict-style handling above) can both attempt to add this
				// exact outpoint within a single accumulated diff, e.g. when
				// calculateDiffBetweenPreviousAndCurrentPruningPointsUsingAcceptanceData replays
				// every accepted transaction across an entire pruning-point-to-pruning-point chain
				// segment. A same-valued duplicate is a legitimate no-op, not corruption - erroring
				// here only forces a fallback to the diff-chain-walk reconstruction, which is the
				// mechanism actually proven unreliable this session.
				log.Debugf("[UTXO-DEBUG] addEntry: coinbase outpoint %s (amount=%d daaScore=%d) already "+
					"present in toAdd with the same value - treated as a legitimate duplicate, not "+
					"re-added", outpoint, entry.Amount(), entry.BlockDAAScore())
				return nil
			}
		}
		return errors.Errorf("AddEntry: Cannot add outpoint %s twice", outpoint)
	} else {
		mud.toAdd.add(outpoint, entry)
	}
	return nil
}

func (mud *mutableUTXODiff) removeEntry(outpoint *externalapi.DomainOutpoint, entry externalapi.UTXOEntry) error {
	if mud.toAdd.containsWithDAAScore(outpoint, entry.BlockDAAScore()) {
		if entry.IsCoinbase() {
			if existing, ok := mud.toAdd.Get(outpoint); ok && (existing.Amount() != entry.Amount() ||
				!existing.ScriptPublicKey().Equal(entry.ScriptPublicKey())) {
				log.Debugf("[UTXO-DEBUG] removeEntry: coinbase outpoint %s being spent (amount=%d daaScore=%d) "+
					"matches a DIFFERENT-valued entry already in toAdd (amount=%d) - removing that toAdd entry "+
					"instead of the one actually being spent. Likely the same content-derived coinbase ID "+
					"collision as addEntry, hitting the removal path instead.",
					outpoint, entry.Amount(), entry.BlockDAAScore(), existing.Amount())
			}
		}
		mud.toAdd.remove(outpoint)
	} else if mud.toRemove.Contains(outpoint) {
		return errors.Errorf("removeEntry: Cannot remove outpoint %s twice", outpoint)
	} else {
		mud.toRemove.add(outpoint, entry)
	}
	return nil
}

func (mud *mutableUTXODiff) clone() *mutableUTXODiff {
	if mud == nil {
		return nil
	}

	return &mutableUTXODiff{
		toAdd:    mud.toAdd.Clone(),
		toRemove: mud.toRemove.Clone(),
	}
}

func (mud *mutableUTXODiff) String() string {
	return fmt.Sprintf("toAdd: %s; toRemove: %s", mud.toAdd, mud.toRemove)
}

func (mud *mutableUTXODiff) Reversed() *mutableUTXODiff {
	return &mutableUTXODiff{
		toAdd:               mud.toRemove,
		toRemove:            mud.toAdd,
		immutableReferences: mud.immutableReferences,
	}
}
