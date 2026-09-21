package exodus

import (
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
)

// EntryDiff describes a single outpoint whose UTXO entry differs between two sources, or is
// present in only one of them.
type EntryDiff struct {
	Outpoint *externalapi.DomainOutpoint

	// InA/InB hold the entry as seen in each source. Exactly one may be nil, meaning the
	// outpoint is missing from that source entirely.
	InA externalapi.UTXOEntry
	InB externalapi.UTXOEntry
}

// DiffResult is the outcome of comparing two UTXO sets.
type DiffResult struct {
	// CountA/CountB are the total number of entries observed in each source.
	CountA uint64
	CountB uint64

	// OnlyInA/OnlyInB are outpoints present in one source but entirely absent from the other.
	OnlyInA []EntryDiff
	OnlyInB []EntryDiff

	// Differing holds outpoints present in both sources but with a different serialized
	// entry (amount, script, DAA score, or coinbase flag).
	Differing []EntryDiff

	// ValueOnlyInA/ValueOnlyInB are the aggregate sompi amount of the outpoints unique to
	// each source (a coarse "how much value is at stake" summary stat).
	ValueOnlyInA uint64
	ValueOnlyInB uint64
}

// Identical returns true if the two sources describe exactly the same UTXO set.
func (d *DiffResult) Identical() bool {
	return len(d.OnlyInA) == 0 && len(d.OnlyInB) == 0 && len(d.Differing) == 0
}

// Diff compares two UTXO sources (each typically a bundle Reader.AsSource() or a live-node
// adapter) and reports outpoints unique to either side plus any outpoints whose entry differs.
//
// Diff builds a single in-memory index (a map keyed by outpoint) over source A, then streams
// source B against it; this makes Diff's memory usage proportional to the size of source A
// (bounded by whichever side is smaller is the caller's choice), rather than requiring both
// full sets to be resident at once. Export/import remain the fully streaming, memory-bounded
// paths; Diff is a comparison/investigation tool where prioritizing simple, obviously-correct
// results over strict memory-boundedness was judged the right tradeoff (see the "exodus
// create/verify/diff" design notes).
func Diff(sourceA, sourceB Source) (*DiffResult, error) {
	type record struct {
		outpoint *externalapi.DomainOutpoint
		entry    externalapi.UTXOEntry
	}

	indexA := make(map[externalapi.DomainOutpoint]externalapi.UTXOEntry)
	result := &DiffResult{}

	err := sourceA(func(outpoint *externalapi.DomainOutpoint, entry externalapi.UTXOEntry) error {
		indexA[*outpoint] = entry
		result.CountA++
		return nil
	})
	if err != nil {
		return nil, err
	}

	err = sourceB(func(outpoint *externalapi.DomainOutpoint, entry externalapi.UTXOEntry) error {
		result.CountB++

		entryA, ok := indexA[*outpoint]
		if !ok {
			result.OnlyInB = append(result.OnlyInB, EntryDiff{Outpoint: outpoint, InB: entry})
			result.ValueOnlyInB += entry.Amount()
			return nil
		}

		if !utxoEntriesEqual(entryA, entry) {
			result.Differing = append(result.Differing, EntryDiff{Outpoint: outpoint, InA: entryA, InB: entry})
		}

		delete(indexA, *outpoint)
		return nil
	})
	if err != nil {
		return nil, err
	}

	for outpoint, entry := range indexA {
		outpoint := outpoint
		result.OnlyInA = append(result.OnlyInA, EntryDiff{Outpoint: &outpoint, InA: entry})
		result.ValueOnlyInA += entry.Amount()
	}

	return result, nil
}

// utxoEntriesEqual compares two UTXO entries for the same outpoint by their serialized form
// (amount, script public key, coinbase flag, accepting block DAA score), i.e. everything that
// participates in the UTXO set commitment.
func utxoEntriesEqual(a, b externalapi.UTXOEntry) bool {
	if a.Amount() != b.Amount() {
		return false
	}
	if a.IsCoinbase() != b.IsCoinbase() {
		return false
	}
	if a.BlockDAAScore() != b.BlockDAAScore() {
		return false
	}
	return a.ScriptPublicKey().Equal(b.ScriptPublicKey())
}
