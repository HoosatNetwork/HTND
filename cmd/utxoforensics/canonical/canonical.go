// Package canonical produces a deterministic, self-describing artefact from a pruning-point UTXO
// set: the set's entry count, its MuHash, and the SHA-256 of a canonical byte encoding.
//
// The point of the artefact is that two people, on two machines, from two copies of a datadir, get
// byte-identical output or find out exactly where they differ. HTN-002 and HTN-005 are both
// arguments about whether a given UTXO set is the right one, conducted so far by comparing numbers
// that were computed slightly differently each time. A canonical encoding makes "the same set"
// checkable.
//
// # What makes it canonical
//
//   - Entries are ordered by outpoint: transaction ID bytes first, then index. Nothing else can
//     affect the order, so input order cannot affect the output.
//   - Each entry is serialized with utxo.SerializeUTXO - the exact function consensus feeds to
//     multiset.Add. That is deliberate: it means the MuHash reported here is the same value the
//     chain's UTXO commitment is compared against, rather than a second opinion computed a
//     different way.
//   - Each entry is length-prefixed, so no two different sets can encode to the same bytes by
//     concatenation.
//   - A fixed header carries a magic string, a format version and the entry count, so a set encoded
//     under a future format cannot silently compare equal to one encoded under this one.
//
// # What it deliberately does not do
//
// It does not decide which pruning point to use, and it does not decide whether the historical
// header commitment or a recomputed one is authoritative. Those are maintainer decisions (HTN-002,
// HTN-005). This only answers "what, exactly, is in this set, and what does it hash to".
package canonical

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"hash"
	"io"
	"sort"

	"github.com/pkg/errors"

	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/multiset"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/utxo"
)

// Magic identifies the encoding, so a file cannot be mistaken for another format's.
const Magic = "HTNUTXO\x00"

// FormatVersion is bumped whenever the encoding changes in any way that would alter the SHA-256 of
// the same logical set. Changing it is a deliberate act: every previously published encoding hash
// stops matching.
const FormatVersion uint32 = 1

// Pair is one UTXO: an outpoint and the entry it maps to.
type Pair struct {
	Outpoint  externalapi.DomainOutpoint
	UTXOEntry externalapi.UTXOEntry
}

// Artifact is the result: what was in the set, and the two hashes that identify it.
type Artifact struct {
	// EntryCount is the number of distinct outpoints after deduplication.
	EntryCount uint64

	// MuHash is the multiset hash over the deduplicated set, computed with the same serialization
	// consensus uses, so it is directly comparable with a pruning point header's UTXOCommitment.
	MuHash *externalapi.DomainHash

	// EncodingSHA256 is the SHA-256 of the canonical encoding. Unlike MuHash it is order-sensitive
	// by construction, which is the point: it detects a set that hashes the same but is not the
	// same sequence of entries, and it is a plain hash anyone can reproduce with sha256sum.
	EncodingSHA256 [sha256.Size]byte
}

// Builder accumulates entries in canonical order and produces an Artifact.
//
// It streams: memory is O(1) in the number of entries, because a mainnet pruning-point set is tens
// of millions of entries and buffering them costs gigabytes. The price is that entries must arrive
// already ordered - Add rejects an out-of-order outpoint rather than accepting it and silently
// producing a non-canonical encoding.
//
// A pebble bucket iterator already yields entries in key order, and the key is the serialized
// outpoint, so the real datadir path satisfies this naturally. BuildFromUnordered handles input
// that does not.
type Builder struct {
	multiset   model.Multiset
	encoding   hash.Hash
	out        io.Writer
	count      uint64
	previous   *externalapi.DomainOutpoint
	headerDone bool
	err        error
}

// NewBuilder starts a Builder. If out is non-nil the canonical encoding is written to it as well as
// hashed, so the artefact can be kept rather than only fingerprinted.
func NewBuilder(out io.Writer) *Builder {
	return &Builder{
		multiset: multiset.New(),
		encoding: sha256.New(),
		out:      out,
	}
}

// Add appends one entry. Outpoints must be strictly increasing: a repeat of the previous outpoint is
// a duplicate and is rejected here rather than silently collapsed, because a set that lists the same
// coin twice is either a bug in whatever produced it or a double-count, and both are findings.
func (b *Builder) Add(pair Pair) error {
	if b.err != nil {
		return b.err
	}
	if !b.headerDone {
		if err := b.writeHeader(); err != nil {
			return b.fail(err)
		}
	}

	if b.previous != nil {
		switch compareOutpoints(b.previous, &pair.Outpoint) {
		case 0:
			return b.fail(errors.Errorf("duplicate outpoint %s:%d - a UTXO set that lists the same "+
				"coin twice is a finding, not something to deduplicate silently",
				&pair.Outpoint.TransactionID, pair.Outpoint.Index))
		case 1:
			return b.fail(errors.Errorf("outpoint %s:%d arrived after %s:%d, so the input is not in "+
				"canonical order; use BuildFromUnordered for input that is not already sorted",
				&pair.Outpoint.TransactionID, pair.Outpoint.Index,
				&b.previous.TransactionID, b.previous.Index))
		}
	}

	serialized, err := utxo.SerializeUTXO(pair.UTXOEntry, &pair.Outpoint)
	if err != nil {
		return b.fail(errors.Wrapf(err, "serializing %s:%d",
			&pair.Outpoint.TransactionID, pair.Outpoint.Index))
	}

	// The same bytes go into both hashes, so the MuHash and the encoding can never describe
	// different content.
	b.multiset.Add(serialized)

	var lengthPrefix [4]byte
	binary.LittleEndian.PutUint32(lengthPrefix[:], uint32(len(serialized)))
	if err := b.write(lengthPrefix[:]); err != nil {
		return err
	}
	if err := b.write(serialized); err != nil {
		return err
	}

	outpoint := pair.Outpoint
	b.previous = &outpoint
	b.count++
	return nil
}

// Finish closes the encoding and returns the artefact.
func (b *Builder) Finish() (*Artifact, error) {
	if b.err != nil {
		return nil, b.err
	}
	if !b.headerDone {
		if err := b.writeHeader(); err != nil {
			return nil, b.fail(err)
		}
	}

	// The count is repeated in a trailer as well as the header. The header's copy is written before
	// anything is known, so on a streamed encoding it is zero; the trailer's is authoritative, and
	// including it means a truncated encoding cannot hash as a shorter valid one.
	var trailer [8]byte
	binary.LittleEndian.PutUint64(trailer[:], b.count)
	if err := b.write(trailer[:]); err != nil {
		return nil, err
	}

	artifact := &Artifact{
		EntryCount: b.count,
		MuHash:     b.multiset.Hash(),
	}
	copy(artifact.EncodingSHA256[:], b.encoding.Sum(nil))
	return artifact, nil
}

func (b *Builder) writeHeader() error {
	b.headerDone = true
	header := make([]byte, 0, len(Magic)+4)
	header = append(header, Magic...)
	var formatVersion [4]byte
	binary.LittleEndian.PutUint32(formatVersion[:], FormatVersion)
	header = append(header, formatVersion[:]...)
	return b.write(header)
}

func (b *Builder) write(p []byte) error {
	if _, err := b.encoding.Write(p); err != nil {
		return b.fail(err)
	}
	if b.out != nil {
		if _, err := b.out.Write(p); err != nil {
			return b.fail(err)
		}
	}
	return nil
}

func (b *Builder) fail(err error) error {
	if b.err == nil {
		b.err = err
	}
	return b.err
}

// BuildFromUnordered sorts pairs into canonical order and builds the artefact.
//
// This is the entry point for input whose order is not already guaranteed - tests, and any caller
// holding a slice. It copies the slice rather than sorting in place, so the caller's ordering is
// left alone and calling it twice on the same slice gives the same answer.
func BuildFromUnordered(pairs []Pair, out io.Writer) (*Artifact, error) {
	ordered := make([]Pair, len(pairs))
	copy(ordered, pairs)
	sort.Slice(ordered, func(i, j int) bool {
		return compareOutpoints(&ordered[i].Outpoint, &ordered[j].Outpoint) < 0
	})

	builder := NewBuilder(out)
	for _, pair := range ordered {
		if err := builder.Add(pair); err != nil {
			return nil, err
		}
	}
	return builder.Finish()
}

// compareOutpoints orders by transaction ID bytes, then by index. It returns -1, 0 or 1.
//
// The byte comparison is DomainTransactionID.Less, which is bytes.Compare over the underlying array
// - not a comparison of hex strings, which HTN-230 removed from the consensus hot paths for being
// 25x slower and allocating two 64-byte strings per comparison. Both orders happen to agree, since
// lowercase hex is monotonic, but there is no reason to pay for the slow one.
func compareOutpoints(a, b *externalapi.DomainOutpoint) int {
	if !a.TransactionID.Equal(&b.TransactionID) {
		if a.TransactionID.Less(&b.TransactionID) {
			return -1
		}
		return 1
	}
	switch {
	case a.Index < b.Index:
		return -1
	case a.Index > b.Index:
		return 1
	default:
		return 0
	}
}

// EncodingHashOf is a convenience for callers that already hold a complete encoding.
func EncodingHashOf(encoding []byte) [sha256.Size]byte {
	return sha256.Sum256(encoding)
}

// Equal reports whether two artefacts describe the same set.
func (a *Artifact) Equal(other *Artifact) bool {
	if a == nil || other == nil {
		return a == other
	}
	return a.EntryCount == other.EntryCount &&
		a.MuHash.Equal(other.MuHash) &&
		bytes.Equal(a.EncodingSHA256[:], other.EncodingSHA256[:])
}
