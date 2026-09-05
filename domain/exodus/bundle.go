// Package exodus implements the tooling used to generate, inspect, and diff candidate
// "exodus pruning point" bundles: community-vetted, manually authored UTXO set checkpoints
// intended to serve as a trusted floor for nodes whose own pruning-point UTXO set
// calculation cannot be trusted (see HoosatNetwork/HTND#21).
//
// This package intentionally does not implement any consensus behavior change, signing, or
// import/rebaseline logic (see HoosatNetwork/HTND#20 for that). It only produces and reads a
// self-contained, on-disk "bundle" directory that other tooling can later consume.
//
// # Bundle format
//
// A bundle is a directory with the following layout:
//
//	<bundle-dir>/
//	  manifest.json      - see Manifest
//	  chunks/
//	    00000000.chunk    - see chunk file format below
//	    00000001.chunk
//	    ...
//
// Chunks contain the UTXO set entries in the order they were produced by the node's own
// past-UTXO-set iteration (domain/consensus's IterateUTXOSetAtBlock, itself backed by
// consensusstatemanager.RestorePastUTXOSetIterator / the pruning point UTXO store). Each
// chunk file is a flat sequence of length-prefixed records:
//
//	[4 bytes little-endian uint32: N]  [N bytes: utxo.SerializeUTXO(entry, outpoint)]
//	...repeated until EOF...
//
// utxo.SerializeUTXO is the exact same serialization already used by the node when
// computing the UTXO set multiset commitment (see pruningmanager.validateUTXOSetFitsCommitment),
// so a chunk's bytes can be fed straight back into a fresh multiset to reproduce the
// commitment without any additional parsing beyond the length-prefix framing.
//
// manifest.json records, for every chunk, its file name, entry count and a SHA-256 digest of
// its raw bytes. This is what makes bundle generation resumable: if `exodus create` is
// interrupted, a subsequent run re-derives the UTXO set from the node (iteration always
// restarts from the beginning - the underlying store does not support a resume cursor for an
// arbitrary historical block) but recognizes already-written, hash-verified chunks from a
// previous attempt and skips re-writing them, only paying the disk write and hashing cost for
// chunks that were not previously completed. This keeps re-runs of `exodus create` on large
// UTXO sets cheap relative to a from-scratch export, as required by the "cheap to iterate
// candidates" goal of the exodus tooling.
//
// A dedicated chunked binary format (as opposed to e.g. a PebbleDB instance) was chosen
// because the artifact is fundamentally write-once/read-a-few-times: a single flat file per
// chunk needs no compaction, has no extra file-count overhead, is trivial to hash and
// distribute (e.g. attach chunk files individually or as a tarball to a GitHub release/issue),
// and streaming sequential reads/writes are exactly what both `exodus create` (write) and a
// future `exodus import` (read) need - there is no requirement for random access by outpoint
// during either operation.
package exodus

import (
	"bufio"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/multiset"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/utxo"
	"github.com/pkg/errors"
)

// FormatVersion is the current on-disk bundle format version. Bump this if the manifest or
// chunk file layout ever changes in an incompatible way.
const FormatVersion = 1

// manifestFileName is the name of the manifest file within a bundle directory.
const manifestFileName = "manifest.json"

// chunksDirName is the name of the sub-directory holding chunk files within a bundle directory.
const chunksDirName = "chunks"

// DefaultChunkEntryCount is the default number of UTXO entries written per chunk file.
const DefaultChunkEntryCount = 500_000

// ChunkInfo describes a single chunk file belonging to a bundle.
type ChunkInfo struct {
	FileName   string `json:"fileName"`
	EntryCount uint64 `json:"entryCount"`
	SHA256     string `json:"sha256"`
}

// Manifest is the metadata describing a candidate exodus bundle.
type Manifest struct {
	FormatVersion int `json:"formatVersion"`

	// ToolVersion and NodeVersion identify the software that produced this bundle, to help
	// with reproducing/debugging discrepancies between independently generated candidates.
	ToolVersion string `json:"toolVersion"`
	NodeVersion string `json:"nodeVersion"`

	// Network is the dagconfig network name (e.g. "hoosat-mainnet") the bundle was generated
	// against.
	Network string `json:"network"`

	// BlockHash and DAAScore identify the block whose UTXO set this bundle captures.
	BlockHash string `json:"blockHash"`
	DAAScore  uint64 `json:"daaScore"`

	// UTXOCommitment is the multiset hash computed over every entry in the bundle, using the
	// same construction as the node's own pruning-point UTXO commitment check
	// (see pruningmanager.validateUTXOSetFitsCommitment).
	UTXOCommitment string `json:"utxoCommitment"`

	// GeneratedAt is the UTC time the bundle was (last) written.
	GeneratedAt time.Time `json:"generatedAt"`

	// OperatorNote is free text supplied by whoever generated the candidate (e.g. identity,
	// rationale, mainnet stall context). No signature or identity verification is performed.
	OperatorNote string `json:"operatorNote"`

	// EntryCount is the total number of UTXO entries across all chunks.
	EntryCount uint64 `json:"entryCount"`

	// Finalized is false while a bundle is still being written (e.g. interrupted export);
	// a manifest with Finalized == false should not be trusted as a complete candidate, but
	// can be used to resume a subsequent `exodus create` run.
	Finalized bool `json:"finalized"`

	// Chunks lists every chunk file making up this bundle, in write order.
	Chunks []ChunkInfo `json:"chunks"`
}

func manifestPath(dir string) string {
	return filepath.Join(dir, manifestFileName)
}

func chunksDir(dir string) string {
	return filepath.Join(dir, chunksDirName)
}

// ReadManifest reads and parses the manifest.json of the bundle at dir.
func ReadManifest(dir string) (*Manifest, error) {
	data, err := os.ReadFile(manifestPath(dir))
	if err != nil {
		return nil, err
	}
	manifest := &Manifest{}
	err = json.Unmarshal(data, manifest)
	if err != nil {
		return nil, errors.Wrapf(err, "malformed manifest at %s", manifestPath(dir))
	}
	return manifest, nil
}

func writeManifest(dir string, manifest *Manifest) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	// Write to a temp file and rename, so a crash mid-write can never leave a corrupt
	// manifest.json behind - the previous (still valid) manifest survives until the new one
	// is fully written.
	tmpPath := manifestPath(dir) + ".tmp"
	err = os.WriteFile(tmpPath, data, 0644)
	if err != nil {
		return err
	}
	return os.Rename(tmpPath, manifestPath(dir))
}

// BundleTarget identifies the block a bundle is being generated for.
type BundleTarget struct {
	BlockHash *externalapi.DomainHash
	DAAScore  uint64
}

// BundleMeta carries the free-form/identifying metadata to stamp onto a finalized bundle.
type BundleMeta struct {
	ToolVersion  string
	NodeVersion  string
	Network      string
	OperatorNote string
}

// Writer streams UTXO set entries into a bundle directory, chunk by chunk, and is able to
// resume a previous, interrupted attempt at exporting the same target block.
type Writer struct {
	dir             string
	target          BundleTarget
	chunkEntryCount int

	multiset model.Multiset

	entryCount      uint64
	resumeSkipCount uint64

	chunks   []ChunkInfo
	curIndex int

	curFile   *os.File
	curWriter *bufio.Writer
	curHash   hash.Hash
	curCount  int
}

// NewWriter opens (or resumes) a bundle directory for writing, targeting the given block.
// chunkEntryCount <= 0 uses DefaultChunkEntryCount.
func NewWriter(dir string, target BundleTarget, chunkEntryCount int) (*Writer, error) {
	if chunkEntryCount <= 0 {
		chunkEntryCount = DefaultChunkEntryCount
	}

	err := os.MkdirAll(chunksDir(dir), 0755)
	if err != nil {
		return nil, err
	}

	w := &Writer{
		dir:             dir,
		target:          target,
		chunkEntryCount: chunkEntryCount,
		multiset:        multiset.New(),
	}

	existing, err := ReadManifest(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return w, nil
		}
		return nil, err
	}

	if existing.Finalized {
		return nil, errors.Errorf(
			"bundle at %s already contains a finalized candidate for block %s (DAA score %d); "+
				"remove it or choose a different directory before creating a new one",
			dir, existing.BlockHash, existing.DAAScore)
	}
	if existing.BlockHash != target.BlockHash.String() {
		return nil, errors.Errorf(
			"bundle at %s has an in-progress export for a different block (%s); "+
				"remove it or choose a different directory before targeting block %s",
			dir, existing.BlockHash, target.BlockHash.String())
	}

	// Recompute the digest of every previously recorded chunk and only trust the contiguous
	// verified prefix; anything after the first mismatch (or missing file) is re-generated.
	for _, chunk := range existing.Chunks {
		digest, count, err := hashAndCountChunkFile(filepath.Join(chunksDir(dir), chunk.FileName))
		if err != nil || digest != chunk.SHA256 || count != chunk.EntryCount {
			break
		}
		w.chunks = append(w.chunks, chunk)
		w.resumeSkipCount += chunk.EntryCount
		w.curIndex++
	}

	return w, nil
}

func hashAndCountChunkFile(path string) (digestHex string, entryCount uint64, err error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()

	hasher := sha256.New()
	reader := bufio.NewReader(io.TeeReader(file, hasher))

	for {
		var length uint32
		err := binary.Read(reader, binary.LittleEndian, &length)
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", 0, err
		}
		_, err = io.CopyN(io.Discard, reader, int64(length))
		if err != nil {
			return "", 0, err
		}
		entryCount++
	}

	return hex.EncodeToString(hasher.Sum(nil)), entryCount, nil
}

// AddEntry adds a single UTXO entry to the bundle. Entries should be added in the same order
// they were produced by the node's UTXO set iteration (this is what RestorePastUTXOSetIterator/
// IterateUTXOSetAtBlock naturally provides).
func (w *Writer) AddEntry(outpoint *externalapi.DomainOutpoint, entry externalapi.UTXOEntry) error {
	serialized, err := utxo.SerializeUTXO(entry, outpoint)
	if err != nil {
		return err
	}

	w.multiset.Add(serialized)
	w.entryCount++

	if w.entryCount <= w.resumeSkipCount {
		// Already durably written and hash-verified in a previous, interrupted attempt.
		return nil
	}

	return w.writeRecord(serialized)
}

func (w *Writer) writeRecord(serialized []byte) error {
	if w.curFile == nil {
		err := w.openChunk()
		if err != nil {
			return err
		}
	}

	var lengthBuf [4]byte
	binary.LittleEndian.PutUint32(lengthBuf[:], uint32(len(serialized)))
	_, err := w.curWriter.Write(lengthBuf[:])
	if err != nil {
		return err
	}
	_, err = w.curWriter.Write(serialized)
	if err != nil {
		return err
	}
	w.curCount++

	if w.curCount >= w.chunkEntryCount {
		return w.closeChunk()
	}
	return nil
}

// chunkFileName returns the stable, lexically-ordered file name for the chunk at the given
// zero-based index.
func chunkFileName(index int) string {
	return fmt.Sprintf("%08d.chunk", index)
}

func (w *Writer) openChunk() error {
	name := chunkFileName(w.curIndex)
	file, err := os.Create(filepath.Join(chunksDir(w.dir), name))
	if err != nil {
		return err
	}
	hasher := sha256.New()
	w.curFile = file
	w.curHash = hasher
	multi := io.MultiWriter(file, hasher)
	w.curWriter = bufio.NewWriter(multi)
	w.curCount = 0
	return nil
}

func (w *Writer) closeChunk() error {
	if w.curFile == nil {
		return nil
	}
	err := w.curWriter.Flush()
	if err != nil {
		return err
	}
	err = w.curFile.Sync()
	if err != nil {
		return err
	}
	name := filepath.Base(w.curFile.Name())
	err = w.curFile.Close()
	if err != nil {
		return err
	}

	sum := w.curHash.Sum(nil)
	w.chunks = append(w.chunks, ChunkInfo{
		FileName:   name,
		EntryCount: uint64(w.curCount),
		SHA256:     hex.EncodeToString(sum),
	})

	w.curIndex++
	w.curFile = nil
	w.curWriter = nil
	w.curHash = nil
	w.curCount = 0
	return nil
}

// Finalize flushes any partial chunk, writes the final manifest.json, and returns the computed
// UTXO set commitment hash.
func (w *Writer) Finalize(meta BundleMeta) (*externalapi.DomainHash, error) {
	err := w.closeChunk()
	if err != nil {
		return nil, err
	}

	commitment := w.multiset.Hash()

	manifest := &Manifest{
		FormatVersion:  FormatVersion,
		ToolVersion:    meta.ToolVersion,
		NodeVersion:    meta.NodeVersion,
		Network:        meta.Network,
		BlockHash:      w.target.BlockHash.String(),
		DAAScore:       w.target.DAAScore,
		UTXOCommitment: commitment.String(),
		GeneratedAt:    time.Now().UTC(),
		OperatorNote:   meta.OperatorNote,
		EntryCount:     w.entryCount,
		Finalized:      true,
		Chunks:         w.chunks,
	}

	// Persist an in-progress manifest first so a crash between the two writes still leaves a
	// resumable (non-finalized) manifest rather than nothing at all. This is best-effort;
	// Finalize is expected to be the last step of a successful `exodus create` run.
	err = writeManifest(w.dir, manifest)
	if err != nil {
		return nil, err
	}

	return commitment, nil
}

// SaveProgress writes a non-finalized manifest reflecting the chunks completed so far, so that
// a subsequent run can resume even if the process is interrupted before Finalize is called.
func (w *Writer) SaveProgress() error {
	manifest := &Manifest{
		FormatVersion: FormatVersion,
		BlockHash:     w.target.BlockHash.String(),
		DAAScore:      w.target.DAAScore,
		GeneratedAt:   time.Now().UTC(),
		EntryCount:    w.entryCount,
		Finalized:     false,
		Chunks:        w.chunks,
	}
	return writeManifest(w.dir, manifest)
}

// EntryCount returns the number of entries added so far (including ones skipped because they
// were already durably written in a previous attempt).
func (w *Writer) EntryCount() uint64 {
	return w.entryCount
}
