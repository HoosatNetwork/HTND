package utxoderive

import (
	"bufio"
	"encoding/binary"
	"io"
	"os"

	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/multiset"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/utxo"
	"github.com/pkg/errors"
	"golang.org/x/crypto/blake2b"
)

// MergeResult is the outcome of combining two pruning-point snapshots.
type MergeResult struct {
	PruningPoint *externalapi.DomainHash

	EntriesA    uint64
	EntriesB    uint64
	SharedCount uint64
	OnlyACount  uint64
	OnlyBCount  uint64

	// ConflictCount is outpoints present in both with DIFFERENT entries. A union cannot contain
	// both, so any conflict makes the union ill-defined and is reported rather than resolved by
	// guessing.
	ConflictCount uint64

	UnionMultiset        *externalapi.DomainHash
	IntersectionMultiset *externalapi.DomainHash
	UnionEntries         uint64
	IntersectionEntries  uint64

	TargetCommitment    *externalapi.DomainHash
	UnionMatches        bool
	IntersectionMatches bool
	AMatches            bool
	BMatches            bool
}

// MergeSnapshots tests whether combining two nodes' pruning-point UTXO sets reconstructs the set
// the pruning point header actually commits to.
//
// The idea is the only one left that does not need bodies nobody has: different nodes were
// measured to be missing DIFFERENT coins at the same pruning point - 511 outpoints on one side
// and 287 on the other in one comparison - so the union of two incomplete exports may be
// complete even though neither is. It costs one pass over each file to find out, against days of
// replay or a governance decision.
//
// Method. MuHash is additive over set union, so the union's hash is A's plus the entries only B
// has, and the intersection's is the entries both share. Neither the union nor the intersection
// is ever materialised in memory: a 16-byte digest per outpoint of the first file is enough to
// classify the second, which keeps a 17-million-entry merge inside a couple of gigabytes.
//
// A conflict - the same outpoint carrying different entries in the two files - makes the union
// ill-defined, so those are counted and reported rather than silently resolved one way.
func MergeSnapshots(pathA, pathB string, target *externalapi.DomainHash) (*MergeResult, error) {
	headerA, err := ReadSnapshotHeader(pathA)
	if err != nil {
		return nil, errors.Wrapf(err, "reading %s", pathA)
	}
	headerB, err := ReadSnapshotHeader(pathB)
	if err != nil {
		return nil, errors.Wrapf(err, "reading %s", pathB)
	}
	if !headerA.PruningPoint.Equal(headerB.PruningPoint) {
		return nil, errors.Errorf("utxoderive: the snapshots are for different pruning points (%s and "+
			"%s). Sets at different heights describe different moments and cannot be combined",
			headerA.PruningPoint, headerB.PruningPoint)
	}

	result := &MergeResult{
		PruningPoint:     headerA.PruningPoint,
		EntriesA:         headerA.EntryCount,
		EntriesB:         headerB.EntryCount,
		TargetCommitment: target,
	}

	// Pass 1: a digest of every outpoint in A, mapped to a digest of its full record, so the
	// second pass can tell "B has this too" from "B has this outpoint with a different entry".
	index := make(map[[16]byte]uint64, headerA.EntryCount)
	unionMultiset := multiset.New()
	err = streamSnapshot(pathA, func(serialized []byte, outpoint *externalapi.DomainOutpoint) error {
		index[outpointDigest(outpoint)] = recordDigest(serialized)
		unionMultiset.Add(serialized)
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Pass 2: classify B, extending the union with what only B has.
	intersectionMultiset := multiset.New()
	err = streamSnapshot(pathB, func(serialized []byte, outpoint *externalapi.DomainOutpoint) error {
		recordHash, present := index[outpointDigest(outpoint)]
		switch {
		case !present:
			result.OnlyBCount++
			unionMultiset.Add(serialized)
		case recordHash == recordDigest(serialized):
			result.SharedCount++
			intersectionMultiset.Add(serialized)
		default:
			result.ConflictCount++
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	result.OnlyACount = headerA.EntryCount - result.SharedCount - result.ConflictCount

	result.UnionEntries = headerA.EntryCount + result.OnlyBCount
	result.IntersectionEntries = result.SharedCount
	result.UnionMultiset = unionMultiset.Hash()
	result.IntersectionMultiset = intersectionMultiset.Hash()

	if target != nil {
		result.UnionMatches = result.UnionMultiset.Equal(target)
		result.IntersectionMatches = result.IntersectionMultiset.Equal(target)
		result.AMatches = headerA.Multiset.Equal(target)
		result.BMatches = headerB.Multiset.Equal(target)
	}
	return result, nil
}

// WriteUnionSnapshot writes the union of two snapshots. Only meaningful once MergeSnapshots has
// shown the union reconstructs the header commitment - writing an unverified union would just
// manufacture a third set nobody can vouch for.
func WriteUnionSnapshot(pathA, pathB, outPath string) (*SnapshotHeader, error) {
	headerA, err := ReadSnapshotHeader(pathA)
	if err != nil {
		return nil, err
	}

	seen := make(map[[16]byte]struct{}, headerA.EntryCount)
	file, err := os.Create(outPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	writer := bufio.NewWriterSize(file, 1<<20)
	if _, err := writer.Write(make([]byte, snapshotHeaderLen)); err != nil {
		return nil, err
	}

	ms := multiset.New()
	count := uint64(0)
	lengthBuffer := make([]byte, 4)
	emit := func(serialized []byte, outpoint *externalapi.DomainOutpoint) error {
		digest := outpointDigest(outpoint)
		if _, already := seen[digest]; already {
			return nil
		}
		seen[digest] = struct{}{}
		ms.Add(serialized)
		binary.LittleEndian.PutUint32(lengthBuffer, uint32(len(serialized)))
		if _, err := writer.Write(lengthBuffer); err != nil {
			return err
		}
		if _, err := writer.Write(serialized); err != nil {
			return err
		}
		count++
		return nil
	}

	if err := streamSnapshot(pathA, emit); err != nil {
		return nil, err
	}
	if err := streamSnapshot(pathB, emit); err != nil {
		return nil, err
	}
	if err := writer.Flush(); err != nil {
		return nil, err
	}

	header := &SnapshotHeader{PruningPoint: headerA.PruningPoint, Multiset: ms.Hash(), EntryCount: count}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	if _, err := file.Write(serializeSnapshotHeader(header)); err != nil {
		return nil, err
	}
	return header, file.Sync()
}

func streamSnapshot(path string, visit func([]byte, *externalapi.DomainOutpoint) error) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, 1<<20)

	headerBuffer := make([]byte, snapshotHeaderLen)
	if _, err := io.ReadFull(reader, headerBuffer); err != nil {
		return err
	}
	header, err := deserializeSnapshotHeader(headerBuffer)
	if err != nil {
		return err
	}

	lengthBuffer := make([]byte, 4)
	for i := uint64(0); i < header.EntryCount; i++ {
		if _, err := io.ReadFull(reader, lengthBuffer); err != nil {
			return errors.Wrapf(err, "snapshot %s ended after %d of %d entries", path, i, header.EntryCount)
		}
		serialized := make([]byte, binary.LittleEndian.Uint32(lengthBuffer))
		if _, err := io.ReadFull(reader, serialized); err != nil {
			return errors.Wrapf(err, "snapshot %s entry %d is truncated", path, i)
		}
		_, outpoint, err := utxo.DeserializeUTXO(serialized)
		if err != nil {
			return errors.Wrapf(err, "snapshot %s entry %d could not be decoded", path, i)
		}
		if err := visit(serialized, outpoint); err != nil {
			return err
		}
	}
	return nil
}

// outpointDigest is a 16-byte digest of an outpoint, computed here rather than taken from the
// database encoding so the merge does not depend on how any particular backend keys its buckets.
func outpointDigest(outpoint *externalapi.DomainOutpoint) [16]byte {
	var buffer [externalapi.DomainHashSize + 4]byte
	copy(buffer[:], outpoint.TransactionID.ByteSlice())
	binary.LittleEndian.PutUint32(buffer[externalapi.DomainHashSize:], outpoint.Index)
	sum := blake2b.Sum256(buffer[:])
	var digest [16]byte
	copy(digest[:], sum[:16])
	return digest
}

func recordDigest(serialized []byte) uint64 {
	sum := blake2b.Sum256(serialized)
	return binary.LittleEndian.Uint64(sum[:8])
}
