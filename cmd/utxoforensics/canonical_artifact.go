package main

import (
	"bufio"
	"fmt"
	"io"
	"os"

	"github.com/HoosatNetwork/HTND/v2/cmd/utxoforensics/canonical"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
)

// canonicalArtifact enumerates the pruning-point UTXO bucket and reports the canonical artefact:
// entry count, MuHash, and the SHA-256 of the canonical encoding.
//
// Read-only. It opens nothing it does not read, writes nothing to the database, and - like every
// other mode here - must be pointed at a COPY of a cleanly shut down datadir. Pebble replays its WAL
// on open, so running this against a live node's directory is not a read at all.
//
// Why both hashes. The MuHash is the value the chain commits to, so it is what a pruning point's
// header commitment can be compared against; it is also order-independent, so it cannot distinguish
// a set from a re-ordered copy of itself. The encoding hash is a plain SHA-256 over a fixed byte
// layout, so anyone can reproduce it with sha256sum against a published file, and it changes if
// anything at all about the sequence changes. Publishing both means "same set" is checkable by
// someone who does not run this tool.
func canonicalArtifact(s *stores, sa *model.StagingArea, encodingPath string) {
	pruningPoint, err := s.pruning.PruningPoint(s.db, sa)
	if err != nil {
		fmt.Printf("pruning point: %v\n", err)
		return
	}

	fmt.Printf("\n=== canonical pruning point UTXO artefact\n")
	fmt.Printf("  pruning point: %s\n", pruningPoint)

	var encodingFile *os.File
	var buffered *bufio.Writer
	if encodingPath != "" {
		encodingFile, err = os.Create(encodingPath)
		if err != nil {
			fmt.Printf("  create %s: %v\n", encodingPath, err)
			return
		}
		defer encodingFile.Close()
		buffered = bufio.NewWriterSize(encodingFile, 1<<20)
	}

	// Declared as the interface and only assigned when there is a real writer. Assigning a nil
	// *bufio.Writer to an io.Writer would produce a non-nil interface holding a nil pointer, which
	// reads as "write the encoding" and panics on the first write.
	var out io.Writer
	if buffered != nil {
		out = buffered
	}

	// The bucket iterator yields entries in key order, and the key is the serialized outpoint, so
	// input arrives already canonical. The builder verifies that rather than assuming it: if the
	// iteration order ever stops matching the encoding's order, this must fail loudly instead of
	// publishing a hash nobody else can reproduce.
	builder := canonical.NewBuilder(out)

	iterator, err := s.pruning.PruningPointUTXOIterator(s.db)
	if err != nil {
		fmt.Printf("  pruning point UTXO iterator: %v\n", err)
		return
	}
	defer iterator.Close()

	for ok := iterator.First(); ok; ok = iterator.Next() {
		outpoint, entry, err := iterator.Get()
		if err != nil {
			fmt.Printf("  iterator.Get: %v\n", err)
			return
		}
		if err := builder.Add(canonical.Pair{Outpoint: *outpoint, UTXOEntry: entry}); err != nil {
			fmt.Printf("  %v\n", err)
			return
		}
	}

	artifact, err := builder.Finish()
	if err != nil {
		fmt.Printf("  %v\n", err)
		return
	}

	if buffered != nil {
		if err := buffered.Flush(); err != nil {
			fmt.Printf("  flush %s: %v\n", encodingPath, err)
			return
		}
		if err := encodingFile.Sync(); err != nil {
			fmt.Printf("  sync %s: %v\n", encodingPath, err)
			return
		}
	}

	fmt.Printf("  entries:       %d\n", artifact.EntryCount)
	fmt.Printf("  muhash:        %s\n", artifact.MuHash)
	fmt.Printf("  encoding-sha256: %x\n", artifact.EncodingSHA256)
	fmt.Printf("  encoding-format: %s v%d\n", "HTNUTXO", canonical.FormatVersion)
	if encodingPath != "" {
		fmt.Printf("  encoding written to: %s\n", encodingPath)
	}

	// Comparing the artefact against the pruning point's own header commitment is the question
	// HTN-002 and HTN-005 are both about, so it is answered here rather than left to the reader.
	// Which of the two values is authoritative is NOT decided here - that is a maintainer decision.
	header, err := s.headers.BlockHeader(s.db, sa, pruningPoint)
	if err != nil {
		fmt.Printf("  (could not read the pruning point header to compare: %v)\n", err)
		return
	}
	commitment := header.UTXOCommitment()
	switch {
	case artifact.MuHash.Equal(commitment):
		fmt.Printf("  => matches the pruning point's header commitment (%s)\n", commitment)
	default:
		fmt.Printf("  => DOES NOT match the pruning point's header commitment\n")
		fmt.Printf("       header commits to: %s\n", commitment)
		fmt.Printf("       this set hashes to: %s\n", artifact.MuHash)
		fmt.Printf("     This node is on an offset UTXO baseline (HTN-002/HTN-005). Whether the\n")
		fmt.Printf("     historical header commitment or a recomputed one is authoritative is a\n")
		fmt.Printf("     maintainer decision; this tool does not make it.\n")
	}
}
