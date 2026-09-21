package exodus

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/utxo"
)

func testOutpoint(t *testing.T, seed byte, index uint32) *externalapi.DomainOutpoint {
	t.Helper()
	var idBytes [externalapi.DomainHashSize]byte
	for i := range idBytes {
		idBytes[i] = seed
	}
	return &externalapi.DomainOutpoint{
		TransactionID: *externalapi.NewDomainTransactionIDFromByteArray(&idBytes),
		Index:         index,
	}
}

func testEntry(amount uint64, daaScore uint64, isCoinbase bool, script byte) externalapi.UTXOEntry {
	return utxo.NewUTXOEntry(amount, &externalapi.ScriptPublicKey{Script: []byte{script}, Version: 0}, isCoinbase, daaScore)
}

func testBlockHash(t *testing.T, seed byte) *externalapi.DomainHash {
	t.Helper()
	var b [externalapi.DomainHashSize]byte
	for i := range b {
		b[i] = seed
	}
	return externalapi.NewDomainHashFromByteArray(&b)
}

func writeTestBundle(t *testing.T, dir string, chunkEntryCount int, count int) *externalapi.DomainHash {
	t.Helper()
	blockHash := testBlockHash(t, 0xAB)

	w, err := NewWriter(dir, BundleTarget{BlockHash: blockHash, DAAScore: 12345}, chunkEntryCount)
	if err != nil {
		t.Fatalf("NewWriter: %s", err)
	}
	for i := 0; i < count; i++ {
		outpoint := testOutpoint(t, byte(i%251+1), uint32(i))
		entry := testEntry(uint64(1000+i), 100, i%7 == 0, byte(i%256))
		err := w.AddEntry(outpoint, entry)
		if err != nil {
			t.Fatalf("AddEntry: %s", err)
		}
	}
	commitment, err := w.Finalize(BundleMeta{ToolVersion: "test", Network: "test-net"})
	if err != nil {
		t.Fatalf("Finalize: %s", err)
	}
	if commitment == nil {
		t.Fatalf("expected non-nil commitment")
	}
	return blockHash
}

func TestBundleRoundTrip(t *testing.T) {
	dir := t.TempDir()
	blockHash := writeTestBundle(t, dir, 10, 37)

	reader, err := OpenBundle(dir)
	if err != nil {
		t.Fatalf("OpenBundle: %s", err)
	}

	manifest := reader.Manifest()
	if !manifest.Finalized {
		t.Fatalf("expected finalized manifest")
	}
	if manifest.BlockHash != blockHash.String() {
		t.Fatalf("block hash mismatch: got %s want %s", manifest.BlockHash, blockHash.String())
	}
	if manifest.EntryCount != 37 {
		t.Fatalf("expected 37 entries, got %d", manifest.EntryCount)
	}
	if len(manifest.Chunks) != 4 { // 37 entries / 10 per chunk = 4 chunks (3 full + 1 partial)
		t.Fatalf("expected 4 chunks, got %d", len(manifest.Chunks))
	}

	var readBack int
	err = reader.Iterate(func(outpoint *externalapi.DomainOutpoint, entry externalapi.UTXOEntry) error {
		readBack++
		return nil
	})
	if err != nil {
		t.Fatalf("Iterate: %s", err)
	}
	if readBack != 37 {
		t.Fatalf("expected to read back 37 entries, got %d", readBack)
	}
}

func TestVerifySelfConsistency(t *testing.T) {
	dir := t.TempDir()
	writeTestBundle(t, dir, 5, 23)

	reader, err := OpenBundle(dir)
	if err != nil {
		t.Fatalf("OpenBundle: %s", err)
	}

	result, err := reader.VerifySelfConsistency()
	if err != nil {
		t.Fatalf("VerifySelfConsistency: %s", err)
	}
	if !result.Matches {
		t.Fatalf("expected bundle to self-verify, got mismatched chunkErrors=%v computed=%s claimed=%s",
			result.ChunkErrors, result.ComputedCommitment, result.ClaimedCommitment)
	}
	if result.EntryCount != 23 {
		t.Fatalf("expected 23 entries verified, got %d", result.EntryCount)
	}
}

func TestVerifySelfConsistencyDetectsCorruption(t *testing.T) {
	dir := t.TempDir()
	writeTestBundle(t, dir, 5, 12)

	// Corrupt the first chunk file by truncating it.
	chunkPath := filepath.Join(chunksDir(dir), "00000000.chunk")
	err := truncateFile(t, chunkPath, 3)
	if err != nil {
		t.Fatalf("truncateFile: %s", err)
	}

	reader, err := OpenBundle(dir)
	if err != nil {
		t.Fatalf("OpenBundle: %s", err)
	}
	result, err := reader.VerifySelfConsistency()
	if err != nil {
		t.Fatalf("VerifySelfConsistency: %s", err)
	}
	if result.Matches {
		t.Fatalf("expected corrupted bundle to fail self-verification")
	}
	if len(result.ChunkErrors) == 0 {
		t.Fatalf("expected chunk errors to be reported")
	}
}

func truncateFile(t *testing.T, path string, size int64) error {
	t.Helper()
	return os.Truncate(path, size)
}

func TestWriterResume(t *testing.T) {
	dir := t.TempDir()
	blockHash := testBlockHash(t, 0xCD)
	target := BundleTarget{BlockHash: blockHash, DAAScore: 999}

	// First "attempt": write 2 full chunks worth of entries, but never Finalize (simulating an
	// interrupted export), and explicitly save progress.
	w1, err := NewWriter(dir, target, 5)
	if err != nil {
		t.Fatalf("NewWriter: %s", err)
	}
	const total = 17
	for i := 0; i < 10; i++ { // exactly two full chunks
		err := w1.AddEntry(testOutpoint(t, byte(i+1), uint32(i)), testEntry(uint64(i), 1, false, 0))
		if err != nil {
			t.Fatalf("AddEntry: %s", err)
		}
	}
	err = w1.SaveProgress()
	if err != nil {
		t.Fatalf("SaveProgress: %s", err)
	}

	manifestBeforeResume, err := ReadManifest(dir)
	if err != nil {
		t.Fatalf("ReadManifest: %s", err)
	}
	if len(manifestBeforeResume.Chunks) != 2 {
		t.Fatalf("expected 2 completed chunks before resume, got %d", len(manifestBeforeResume.Chunks))
	}

	// Second "attempt": resume from scratch, replaying ALL entries from the beginning (as a
	// real re-run against RestorePastUTXOSetIterator would, since there is no resume cursor),
	// and confirm the writer recognizes the first 10 as already durably written.
	w2, err := NewWriter(dir, target, 5)
	if err != nil {
		t.Fatalf("NewWriter (resume): %s", err)
	}
	if w2.resumeSkipCount != 10 {
		t.Fatalf("expected resumeSkipCount=10, got %d", w2.resumeSkipCount)
	}
	for i := 0; i < total; i++ {
		err := w2.AddEntry(testOutpoint(t, byte(i+1), uint32(i)), testEntry(uint64(i), 1, false, 0))
		if err != nil {
			t.Fatalf("AddEntry: %s", err)
		}
	}
	commitment, err := w2.Finalize(BundleMeta{ToolVersion: "test"})
	if err != nil {
		t.Fatalf("Finalize: %s", err)
	}

	reader, err := OpenBundle(dir)
	if err != nil {
		t.Fatalf("OpenBundle: %s", err)
	}
	result, err := reader.VerifySelfConsistency()
	if err != nil {
		t.Fatalf("VerifySelfConsistency: %s", err)
	}
	if !result.Matches {
		t.Fatalf("resumed bundle failed self-verification: %v", result.ChunkErrors)
	}
	if result.EntryCount != total {
		t.Fatalf("expected %d entries after resume, got %d", total, result.EntryCount)
	}
	if !commitment.Equal(result.ComputedCommitment) {
		t.Fatalf("commitment mismatch after resume")
	}
}

func TestDiffIdentical(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	writeTestBundle(t, dirA, 4, 15)
	// Same content, different directory/chunk size, should still diff to identical.
	blockHash := testBlockHash(t, 0xAB)
	w, err := NewWriter(dirB, BundleTarget{BlockHash: blockHash, DAAScore: 12345}, 6)
	if err != nil {
		t.Fatalf("NewWriter: %s", err)
	}
	for i := 0; i < 15; i++ {
		err := w.AddEntry(testOutpoint(t, byte(i%251+1), uint32(i)), testEntry(uint64(1000+i), 100, i%7 == 0, byte(i%256)))
		if err != nil {
			t.Fatalf("AddEntry: %s", err)
		}
	}
	_, err = w.Finalize(BundleMeta{})
	if err != nil {
		t.Fatalf("Finalize: %s", err)
	}

	readerA, err := OpenBundle(dirA)
	if err != nil {
		t.Fatalf("OpenBundle A: %s", err)
	}
	readerB, err := OpenBundle(dirB)
	if err != nil {
		t.Fatalf("OpenBundle B: %s", err)
	}

	result, err := Diff(readerA.AsSource(), readerB.AsSource())
	if err != nil {
		t.Fatalf("Diff: %s", err)
	}
	if !result.Identical() {
		t.Fatalf("expected identical sets, got onlyInA=%d onlyInB=%d differing=%d",
			len(result.OnlyInA), len(result.OnlyInB), len(result.Differing))
	}
	if result.CountA != 15 || result.CountB != 15 {
		t.Fatalf("expected counts of 15, got A=%d B=%d", result.CountA, result.CountB)
	}
}

func TestDiffDetectsDivergence(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	blockHash := testBlockHash(t, 0xEF)

	wA, err := NewWriter(dirA, BundleTarget{BlockHash: blockHash, DAAScore: 1}, 100)
	if err != nil {
		t.Fatalf("NewWriter A: %s", err)
	}
	// outpoint 0: amount 10 (only in A with this value)
	// outpoint 1: identical in both
	// outpoint 2: only in A
	err = wA.AddEntry(testOutpoint(t, 1, 0), testEntry(10, 1, false, 0))
	if err != nil {
		t.Fatal(err)
	}
	err = wA.AddEntry(testOutpoint(t, 2, 1), testEntry(20, 1, false, 0))
	if err != nil {
		t.Fatal(err)
	}
	err = wA.AddEntry(testOutpoint(t, 3, 2), testEntry(30, 1, false, 0))
	if err != nil {
		t.Fatal(err)
	}
	_, err = wA.Finalize(BundleMeta{})
	if err != nil {
		t.Fatalf("Finalize A: %s", err)
	}

	wB, err := NewWriter(dirB, BundleTarget{BlockHash: blockHash, DAAScore: 1}, 100)
	if err != nil {
		t.Fatalf("NewWriter B: %s", err)
	}
	// outpoint 0: amount 999 (differing value)
	// outpoint 1: identical
	// outpoint 3: only in B
	err = wB.AddEntry(testOutpoint(t, 1, 0), testEntry(999, 1, false, 0))
	if err != nil {
		t.Fatal(err)
	}
	err = wB.AddEntry(testOutpoint(t, 2, 1), testEntry(20, 1, false, 0))
	if err != nil {
		t.Fatal(err)
	}
	err = wB.AddEntry(testOutpoint(t, 4, 3), testEntry(40, 1, false, 0))
	if err != nil {
		t.Fatal(err)
	}
	_, err = wB.Finalize(BundleMeta{})
	if err != nil {
		t.Fatalf("Finalize B: %s", err)
	}

	readerA, err := OpenBundle(dirA)
	if err != nil {
		t.Fatalf("OpenBundle A: %s", err)
	}
	readerB, err := OpenBundle(dirB)
	if err != nil {
		t.Fatalf("OpenBundle B: %s", err)
	}

	result, err := Diff(readerA.AsSource(), readerB.AsSource())
	if err != nil {
		t.Fatalf("Diff: %s", err)
	}
	if result.Identical() {
		t.Fatalf("expected sets to diverge")
	}
	if len(result.Differing) != 1 {
		t.Fatalf("expected exactly 1 differing entry, got %d", len(result.Differing))
	}
	if len(result.OnlyInA) != 1 || len(result.OnlyInB) != 1 {
		t.Fatalf("expected exactly 1 only-in-A and 1 only-in-B, got %d and %d",
			len(result.OnlyInA), len(result.OnlyInB))
	}
	if result.ValueOnlyInA != 30 || result.ValueOnlyInB != 40 {
		t.Fatalf("unexpected aggregate values: onlyInA=%d onlyInB=%d", result.ValueOnlyInA, result.ValueOnlyInB)
	}
}
