package exodus

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/multiset"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/utxo"
	"github.com/pkg/errors"
)

// EntryCallback is invoked once per UTXO entry while reading a bundle or a live consensus
// UTXO set.
type EntryCallback func(outpoint *externalapi.DomainOutpoint, entry externalapi.UTXOEntry) error

// Source produces a stream of UTXO entries by invoking callback once per entry. It is
// implemented both by Reader.Iterate (reading a persisted bundle) and by adapters around
// externalapi.Consensus.IterateUTXOSetAtBlock (reading a live node), so `exodus verify` and
// `exodus diff` can treat "a bundle" and "a live recomputation" uniformly.
type Source func(callback EntryCallback) error

// Reader reads a previously written bundle directory.
type Reader struct {
	dir      string
	manifest *Manifest
}

// OpenBundle opens an existing bundle directory for reading, parsing its manifest.
func OpenBundle(dir string) (*Reader, error) {
	manifest, err := ReadManifest(dir)
	if err != nil {
		return nil, err
	}
	return &Reader{dir: dir, manifest: manifest}, nil
}

// Manifest returns the parsed manifest.json of this bundle.
func (r *Reader) Manifest() *Manifest {
	return r.manifest
}

// Iterate streams every UTXO entry in the bundle, in on-disk (chunk, then in-chunk) order,
// without verifying per-chunk hashes. Use VerifySelfConsistency to additionally validate
// integrity.
func (r *Reader) Iterate(callback EntryCallback) error {
	for _, chunk := range r.manifest.Chunks {
		err := iterateChunkFile(filepath.Join(chunksDir(r.dir), chunk.FileName), callback)
		if err != nil {
			return errors.Wrapf(err, "failed reading chunk %s", chunk.FileName)
		}
	}
	return nil
}

func iterateChunkFile(path string, callback EntryCallback) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	reader := bufio.NewReader(file)
	for {
		var length uint32
		err := binary.Read(reader, binary.LittleEndian, &length)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}

		buf := make([]byte, length)
		_, err = io.ReadFull(reader, buf)
		if err != nil {
			return err
		}

		entry, outpoint, err := utxo.DeserializeUTXO(buf)
		if err != nil {
			return err
		}

		err = callback(outpoint, entry)
		if err != nil {
			return err
		}
	}
}

// VerifyResult is the outcome of checking a bundle's internal self-consistency.
type VerifyResult struct {
	// Matches is true if the recomputed commitment equals the manifest's claimed commitment.
	Matches bool

	// ComputedCommitment is the multiset hash recomputed by re-reading every chunk.
	ComputedCommitment *externalapi.DomainHash

	// ClaimedCommitment is the commitment recorded in the manifest.
	ClaimedCommitment *externalapi.DomainHash

	// EntryCount is the number of entries actually read back from the chunk files.
	EntryCount uint64

	// ChunkErrors records any chunk whose recomputed SHA-256 digest or entry count did not
	// match what the manifest recorded (data corruption/truncation).
	ChunkErrors []string
}

// VerifySelfConsistency re-reads every chunk of the bundle, checks each chunk's SHA-256 digest
// and entry count against the manifest, recomputes the multiset commitment over every entry,
// and compares it against the manifest's claimed commitment.
func (r *Reader) VerifySelfConsistency() (*VerifyResult, error) {
	if !r.manifest.Finalized {
		return nil, errors.Errorf(
			"bundle at %s is not finalized (looks like an interrupted `exodus create` run); nothing to verify yet",
			r.dir)
	}

	result := &VerifyResult{}
	ms := multiset.New()

	for _, chunk := range r.manifest.Chunks {
		path := filepath.Join(chunksDir(r.dir), chunk.FileName)
		digest, count, err := hashAndCountChunkFile(path)
		if err != nil {
			result.ChunkErrors = append(result.ChunkErrors,
				fmt.Sprintf("chunk %s: failed to read: %s", chunk.FileName, err))
			continue
		}
		if digest != chunk.SHA256 {
			result.ChunkErrors = append(result.ChunkErrors,
				fmt.Sprintf("chunk %s: SHA-256 mismatch (manifest %s, actual %s)", chunk.FileName, chunk.SHA256, digest))
		}
		if count != chunk.EntryCount {
			result.ChunkErrors = append(result.ChunkErrors,
				fmt.Sprintf("chunk %s: entry count mismatch (manifest %d, actual %d)", chunk.FileName, chunk.EntryCount, count))
		}
	}

	err := r.Iterate(func(outpoint *externalapi.DomainOutpoint, entry externalapi.UTXOEntry) error {
		serialized, err := utxo.SerializeUTXO(entry, outpoint)
		if err != nil {
			return err
		}
		ms.Add(serialized)
		result.EntryCount++
		return nil
	})
	if err != nil {
		// A chunk that failed to hash/count above (already recorded in ChunkErrors) will
		// also fail here since it's the same malformed data; report it as a further
		// self-consistency failure rather than aborting verification entirely.
		result.ChunkErrors = append(result.ChunkErrors, fmt.Sprintf("failed to fully read bundle: %s", err))
		result.Matches = false
		return result, nil
	}

	result.ComputedCommitment = ms.Hash()
	claimed, err := externalapi.NewDomainHashFromString(r.manifest.UTXOCommitment)
	if err != nil {
		return nil, errors.Wrapf(err, "manifest has malformed UTXO commitment %q", r.manifest.UTXOCommitment)
	}
	result.ClaimedCommitment = claimed
	result.Matches = result.ComputedCommitment.Equal(claimed) && len(result.ChunkErrors) == 0 &&
		result.EntryCount == r.manifest.EntryCount

	return result, nil
}

// AsSource adapts this bundle Reader to the Source function type used by Diff.
func (r *Reader) AsSource() Source {
	return r.Iterate
}
