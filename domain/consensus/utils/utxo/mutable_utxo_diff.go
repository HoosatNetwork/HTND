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

// sameCoin reports whether two entries at one outpoint describe the same coin. containsWithDAAScore
// matches on (outpoint, BlockDAAScore) alone, which is NOT an identity for a UTXO: two entries can
// share both and still be different coins carrying different amounts or scripts. Cancelling one
// against the other on that partial match is what silently loses coins from a diff - and, once that
// diff is applied to the materialised UTXO set, from the node's UTXO set permanently.
// sameDAAScore reports whether two entries at one outpoint agree on the block that merged the coin.
// Kept separate from sameCoin because they answer different questions: sameCoin asks whether this is
// the same coin at all, and this asks whether the two records of it agree on when it came into the
// set - which is part of the commitment preimage and therefore not a detail.
func sameDAAScore(a, b externalapi.UTXOEntry) bool {
	return a != nil && b != nil && a.BlockDAAScore() == b.BlockDAAScore()
}

func sameCoin(a, b externalapi.UTXOEntry) bool {
	return a != nil && b != nil && a.Amount() == b.Amount() && a.IsCoinbase() == b.IsCoinbase() &&
		a.ScriptPublicKey().Equal(b.ScriptPublicKey())
}

func (mud *mutableUTXODiff) addEntry(outpoint *externalapi.DomainOutpoint, entry externalapi.UTXOEntry) error {
	if mud.toRemove.containsWithDAAScore(outpoint, entry.BlockDAAScore()) {
		existing, _ := mud.toRemove.Get(outpoint)
		if !sameCoin(existing, entry) {
			// The pending removal is for a different coin that merely shares this outpoint and DAA
			// score. Cancelling the two against each other drops this addition entirely, which loses a
			// real, freshly created output from the diff - and from the UTXO set once the diff is
			// applied. Discard the entry that cannot be right and record what this call was told to do.
			log.Debugf("[UTXO-DEBUG] addEntry: outpoint %s (amount=%d daaScore=%d isCoinbase=%t) collided "+
				"with a DIFFERENT-valued pre-existing toRemove entry (amount=%d) - these are distinct coins, "+
				"so record the addition instead of silently cancelling it out",
				outpoint, entry.Amount(), entry.BlockDAAScore(), entry.IsCoinbase(), existing.Amount())
			mud.toRemove.remove(outpoint)
			mud.toAdd.add(outpoint, entry)
			return nil
		}
		// Same coin: this addition genuinely cancels the pending removal.
		mud.toRemove.remove(outpoint)
	} else if mud.toAdd.Contains(outpoint) {
		if existing, ok := mud.toAdd.Get(outpoint); ok {
			if sameCoin(existing, entry) {
				// A set holds a coin once. Adding an outpoint that is already pending with the SAME
				// amount, script and coinbase flag is therefore a no-op on the set, not corruption -
				// and MuHash agrees: ApplyAcceptanceDataToMultiset skips exactly these duplicates so
				// the multiset stays the hash of a set. The diff has to make the same choice, or the
				// two quantities that must match by construction are computed under different rules.
				//
				// This used to be allowed only for coinbases, on the theory that only byte-identical
				// coinbases can share a transaction ID. That was too narrow. A transaction ID is
				// content-derived, so ANY two entries at one outpoint carrying the same value are the
				// same coin, whatever produced them - and a block re-merged into virtual after its
				// transaction is already reflected there presents precisely that. Refusing it aborted
				// the whole virtual update, which is not a recoverable condition: the block is
				// re-requested, fails identically, and the node retries forever without ever
				// advancing. One such coin stalled a node's IBD 61 times against six different peers.
				//
				// When the two carry DIFFERENT BlockDAAScores, the incoming one wins rather than the
				// one already pending.
				//
				// Keeping the first looks like the safe choice and is not. A coin's stamp is the DAA
				// score of the block that merged it, and a block's own acceptance data is authoritative
				// for its own past: within one accumulated diff, every entry acceptance creates is
				// stamped with that block's score. So an incoming entry whose score differs from a
				// pending one is a RESTAMP - the diff was seeded with the coin at an older merging
				// block's score, and this block is now the one merging it.
				//
				// Discarding that left the diff describing the coin differently from the acceptance
				// data beside it, and blockOnlyCarriesTheInheritedOffset correctly refuses to tolerate
				// a block whose two records of itself disagree. The block is then disqualified, and a
				// node hits it on every retry: "output 0 is in the block's past UTXO set with different
				// contents than its acceptance data describes", forever.
				if !sameDAAScore(existing, entry) {
					log.Warnf("[UTXO-DEBUG] addEntry: outpoint %s is pending in toAdd at daaScore=%d and "+
						"is being added again at daaScore=%d with the same value - taking the incoming "+
						"score, because this block's acceptance data is what its commitment is computed "+
						"from", outpoint, existing.BlockDAAScore(), entry.BlockDAAScore())
					mud.toAdd.remove(outpoint)
					mud.toAdd.add(outpoint, entry)
					return nil
				}
				if entry.IsCoinbase() {
					log.Debugf("[UTXO-DEBUG] addEntry: coinbase outpoint %s (amount=%d daaScore=%d) already "+
						"present in toAdd with the same value - treated as a legitimate duplicate, not "+
						"re-added", outpoint, entry.Amount(), entry.BlockDAAScore())
				} else {
					// Warn, not debug: unlike the coinbase case this is not routine, it is the shape
					// that deadlocked IBD, and a node running at the default log level has to be able
					// to see it happening without being rebuilt.
					log.Warnf("[UTXO-DEBUG] addEntry: outpoint %s (amount=%d daaScore=%d) is already "+
						"pending in toAdd with the same value (daaScore=%d) - same coin, so the "+
						"duplicate add is a no-op rather than an error",
						outpoint, entry.Amount(), entry.BlockDAAScore(), existing.BlockDAAScore())
				}
				return nil
			}
			// Different values at one outpoint. That is not a duplicate of anything, and silently
			// picking either one would corrupt the set, so it stays an error - but say what the two
			// coins actually were, because "cannot add twice" alone gives nothing to diagnose from.
			return errors.Errorf("AddEntry: Cannot add outpoint %s twice: pending entry has "+
				"amount=%d daaScore=%d isCoinbase=%t, incoming entry has amount=%d daaScore=%d "+
				"isCoinbase=%t", outpoint, existing.Amount(), existing.BlockDAAScore(),
				existing.IsCoinbase(), entry.Amount(), entry.BlockDAAScore(), entry.IsCoinbase())
		}
		return errors.Errorf("AddEntry: Cannot add outpoint %s twice", outpoint)
	} else {
		mud.toAdd.add(outpoint, entry)
	}
	return nil
}

func (mud *mutableUTXODiff) removeEntry(outpoint *externalapi.DomainOutpoint, entry externalapi.UTXOEntry) error {
	if mud.toAdd.containsWithDAAScore(outpoint, entry.BlockDAAScore()) {
		existing, _ := mud.toAdd.Get(outpoint)
		if !sameCoin(existing, entry) {
			// The coin being spent is not the coin sitting in toAdd - they only share an outpoint and a
			// DAA score. Dropping the toAdd entry and stopping there, as this used to do, records no
			// removal at all, so the coin that was actually spent survives into the resulting UTXO set
			// and stays spendable forever. Mirror addEntry's handling of the same collision: discard the
			// entry that cannot be right and record what this call was actually told to do.
			log.Debugf("[UTXO-DEBUG] removeEntry: outpoint %s being spent (amount=%d daaScore=%d "+
				"isCoinbase=%t) collided with a DIFFERENT-valued entry in toAdd (amount=%d) - these are "+
				"distinct coins, so record the removal instead of dropping it",
				outpoint, entry.Amount(), entry.BlockDAAScore(), entry.IsCoinbase(), existing.Amount())
			mud.toAdd.remove(outpoint)
			mud.toRemove.add(outpoint, entry)
			return nil
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
