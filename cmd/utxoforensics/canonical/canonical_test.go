package canonical

import (
	"bytes"
	"math/rand"
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/multiset"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/utxo"
)

func testPair(seed byte, index uint32, amount uint64) Pair {
	var idBytes [externalapi.DomainHashSize]byte
	for i := range idBytes {
		idBytes[i] = seed + byte(i)
	}
	return Pair{
		Outpoint: externalapi.DomainOutpoint{
			TransactionID: *externalapi.NewDomainTransactionIDFromByteArray(&idBytes),
			Index:         index,
		},
		UTXOEntry: utxo.NewUTXOEntry(
			amount,
			&externalapi.ScriptPublicKey{Script: []byte{0x51, seed}, Version: 0},
			seed%2 == 0,
			uint64(seed)*1000,
		),
	}
}

// testSet builds a set with several transaction IDs and several indexes each, so that both halves of
// the ordering - the ID bytes and the index - are actually exercised.
func testSet() []Pair {
	pairs := make([]Pair, 0, 64)
	for seed := byte(1); seed <= 16; seed++ {
		for index := uint32(0); index < 4; index++ {
			pairs = append(pairs, testPair(seed, index, uint64(seed)*100+uint64(index)))
		}
	}
	return pairs
}

func buildOrFail(t *testing.T, pairs []Pair) (*Artifact, []byte) {
	t.Helper()
	encoding := &bytes.Buffer{}
	artifact, err := BuildFromUnordered(pairs, encoding)
	if err != nil {
		t.Fatalf("BuildFromUnordered: %+v", err)
	}
	return artifact, encoding.Bytes()
}

// TestRepeatedRunsProduceIdenticalOutput is the first half of the determinism requirement. Nothing
// in the artefact may depend on map iteration order, timing, or anything else that varies between
// runs - if it did, two people checking the same datadir would disagree and have no way to tell a
// real difference from noise.
func TestRepeatedRunsProduceIdenticalOutput(t *testing.T) {
	pairs := testSet()

	first, firstEncoding := buildOrFail(t, pairs)
	for run := range 8 {
		next, nextEncoding := buildOrFail(t, pairs)
		if !first.Equal(next) {
			t.Fatalf("run %d produced a different artefact: count %d/%d, muhash %s/%s, sha256 %x/%x",
				run, first.EntryCount, next.EntryCount, first.MuHash, next.MuHash,
				first.EncodingSHA256, next.EncodingSHA256)
		}
		if !bytes.Equal(firstEncoding, nextEncoding) {
			t.Fatalf("run %d produced different encoding bytes", run)
		}
	}
}

// TestDifferentlyOrderedInputsProduceIdenticalOutput is the second half, and the one that actually
// matters in practice: the same set read from two datadirs, or two tools, will not arrive in the
// same order, and the artefact must not care.
func TestDifferentlyOrderedInputsProduceIdenticalOutput(t *testing.T) {
	pairs := testSet()
	expected, expectedEncoding := buildOrFail(t, pairs)

	random := rand.New(rand.NewSource(1))
	for shuffle := range 16 {
		shuffled := make([]Pair, len(pairs))
		copy(shuffled, pairs)
		random.Shuffle(len(shuffled), func(i, j int) {
			shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
		})

		got, gotEncoding := buildOrFail(t, shuffled)
		if !expected.Equal(got) {
			t.Fatalf("shuffle %d changed the artefact: muhash %s vs %s, sha256 %x vs %x",
				shuffle, expected.MuHash, got.MuHash, expected.EncodingSHA256, got.EncodingSHA256)
		}
		if !bytes.Equal(expectedEncoding, gotEncoding) {
			t.Fatalf("shuffle %d changed the encoding bytes", shuffle)
		}
	}
}

// TestBuildFromUnorderedDoesNotReorderTheCaller'sSlice - a tool that quietly sorts its caller's data
// is a trap, and it would also make a second call on the same slice take a different path.
func TestBuildFromUnorderedLeavesTheCallersSliceAlone(t *testing.T) {
	pairs := testSet()
	random := rand.New(rand.NewSource(7))
	random.Shuffle(len(pairs), func(i, j int) { pairs[i], pairs[j] = pairs[j], pairs[i] })

	before := make([]Pair, len(pairs))
	copy(before, pairs)

	if _, err := BuildFromUnordered(pairs, nil); err != nil {
		t.Fatalf("BuildFromUnordered: %+v", err)
	}

	for i := range pairs {
		if compareOutpoints(&before[i].Outpoint, &pairs[i].Outpoint) != 0 {
			t.Fatalf("the caller's slice was reordered at index %d", i)
		}
	}
}

// TestOneSompiChangesBothHashes is the property HTN-002 and HTN-005 need. The smallest possible
// difference - one sompi on one entry, every outpoint still present - has to be visible. A set that
// is short by one coin is exactly what an incomplete pruning-point snapshot produces.
func TestOneSompiChangesBothHashes(t *testing.T) {
	pairs := testSet()
	original, originalEncoding := buildOrFail(t, pairs)

	modified := make([]Pair, len(pairs))
	copy(modified, pairs)
	victim := modified[len(modified)/2]
	modified[len(modified)/2] = Pair{
		Outpoint: victim.Outpoint,
		UTXOEntry: utxo.NewUTXOEntry(
			victim.UTXOEntry.Amount()-1,
			victim.UTXOEntry.ScriptPublicKey(),
			victim.UTXOEntry.IsCoinbase(),
			victim.UTXOEntry.BlockDAAScore(),
		),
	}

	changed, changedEncoding := buildOrFail(t, modified)

	if changed.EntryCount != original.EntryCount {
		t.Fatalf("entry count changed (%d vs %d) - the test modified membership, not just a value",
			original.EntryCount, changed.EntryCount)
	}
	if original.MuHash.Equal(changed.MuHash) {
		t.Errorf("removing one sompi did not change the MuHash (%s) - the artefact cannot detect "+
			"an incomplete set", original.MuHash)
	}
	if original.EncodingSHA256 == changed.EncodingSHA256 {
		t.Errorf("removing one sompi did not change the encoding hash (%x)", original.EncodingSHA256)
	}
	if bytes.Equal(originalEncoding, changedEncoding) {
		t.Error("removing one sompi did not change the encoding bytes")
	}
}

// TestMissingEntryChangesBothHashes covers the other shape of incompleteness: an outpoint that is
// simply absent.
func TestMissingEntryChangesBothHashes(t *testing.T) {
	pairs := testSet()
	full, _ := buildOrFail(t, pairs)

	short := append([]Pair{}, pairs[:len(pairs)-1]...)
	truncated, _ := buildOrFail(t, short)

	if truncated.EntryCount != full.EntryCount-1 {
		t.Fatalf("expected one fewer entry, got %d vs %d", truncated.EntryCount, full.EntryCount)
	}
	if full.MuHash.Equal(truncated.MuHash) {
		t.Error("dropping an entry did not change the MuHash")
	}
	if full.EncodingSHA256 == truncated.EncodingSHA256 {
		t.Error("dropping an entry did not change the encoding hash")
	}
}

// TestMuHashMatchesTheConsensusComputation is what makes the reported MuHash worth anything: it has
// to be the same value a pruning point header's UTXOCommitment is compared against, not a second
// opinion computed a different way. Here it is cross-checked against a multiset built directly with
// the same calls consensus's addUTXOToMultiset makes.
func TestMuHashMatchesTheConsensusComputation(t *testing.T) {
	pairs := testSet()
	artifact, _ := buildOrFail(t, pairs)

	independent := multiset.New()
	for _, pair := range pairs {
		outpoint := pair.Outpoint
		serialized, err := utxo.SerializeUTXO(pair.UTXOEntry, &outpoint)
		if err != nil {
			t.Fatalf("SerializeUTXO: %+v", err)
		}
		independent.Add(serialized)
	}

	if !artifact.MuHash.Equal(independent.Hash()) {
		t.Fatalf("the artefact's MuHash (%s) is not the multiset consensus would compute (%s)",
			artifact.MuHash, independent.Hash())
	}
}

// TestDuplicateOutpointIsRejected pins that a repeated coin is surfaced rather than deduplicated.
// Silently collapsing it would turn a double-count - one of the two things that actually went wrong
// in the imported pruning point sets this tool exists to examine - into a clean-looking artefact.
func TestDuplicateOutpointIsRejected(t *testing.T) {
	pairs := testSet()
	withDuplicate := append(pairs, pairs[3])

	if _, err := BuildFromUnordered(withDuplicate, nil); err == nil {
		t.Fatal("a duplicated outpoint was accepted")
	}
}

// TestBuilderRejectsOutOfOrderInput pins the streaming contract. Builder cannot buffer - a mainnet
// set is tens of millions of entries - so it must refuse input it cannot canonicalise rather than
// emit a non-canonical encoding that looks fine.
func TestBuilderRejectsOutOfOrderInput(t *testing.T) {
	pairs := testSet()
	builder := NewBuilder(nil)

	// Feed the last one first, then the first: guaranteed out of order.
	if err := builder.Add(pairs[len(pairs)-1]); err != nil {
		t.Fatalf("first Add: %+v", err)
	}
	if err := builder.Add(pairs[0]); err == nil {
		t.Fatal("Builder accepted an out-of-order outpoint")
	}
}

// TestStreamingAndSortingAgree proves the two entry points cannot drift apart: pre-sorted input fed
// to Builder must give exactly what BuildFromUnordered gives.
func TestStreamingAndSortingAgree(t *testing.T) {
	pairs := testSet()

	sorted, sortedEncoding := buildOrFail(t, pairs)

	// Feed the same pairs to a raw Builder in canonical order.
	ordered := make([]Pair, len(pairs))
	copy(ordered, pairs)
	for i := 0; i < len(ordered); i++ {
		for j := i + 1; j < len(ordered); j++ {
			if compareOutpoints(&ordered[j].Outpoint, &ordered[i].Outpoint) < 0 {
				ordered[i], ordered[j] = ordered[j], ordered[i]
			}
		}
	}

	streamEncoding := &bytes.Buffer{}
	builder := NewBuilder(streamEncoding)
	for _, pair := range ordered {
		if err := builder.Add(pair); err != nil {
			t.Fatalf("Add: %+v", err)
		}
	}
	streamed, err := builder.Finish()
	if err != nil {
		t.Fatalf("Finish: %+v", err)
	}

	if !sorted.Equal(streamed) {
		t.Fatalf("the streaming and sorting entry points disagree: sha256 %x vs %x",
			sorted.EncodingSHA256, streamed.EncodingSHA256)
	}
	if !bytes.Equal(sortedEncoding, streamEncoding.Bytes()) {
		t.Fatal("the streaming and sorting entry points produced different encoding bytes")
	}
}

// TestEncodingIsSelfDescribing checks the header and the trailer are actually there. The magic and
// format version mean a future encoding change cannot silently compare equal to this one, and the
// trailing count means a truncated file cannot hash as a shorter valid one.
func TestEncodingIsSelfDescribing(t *testing.T) {
	artifact, encoding := buildOrFail(t, testSet())

	if !bytes.HasPrefix(encoding, []byte(Magic)) {
		t.Fatalf("encoding does not start with the magic %q", Magic)
	}
	if EncodingHashOf(encoding) != artifact.EncodingSHA256 {
		t.Fatal("the reported SHA-256 is not the hash of the encoding that was written")
	}

	// Truncating must change the hash, which the trailing count guarantees even if the truncation
	// happens to land on an entry boundary.
	if EncodingHashOf(encoding[:len(encoding)-8]) == artifact.EncodingSHA256 {
		t.Fatal("a truncated encoding hashes the same as the complete one")
	}
}

// TestEmptySetIsStillWellDefined - an empty pruning point set is a finding, not a crash, and it must
// still produce a stable, reproducible artefact.
func TestEmptySetIsStillWellDefined(t *testing.T) {
	first, firstEncoding := buildOrFail(t, nil)
	second, secondEncoding := buildOrFail(t, []Pair{})

	if first.EntryCount != 0 {
		t.Fatalf("empty set reported %d entries", first.EntryCount)
	}
	if !first.Equal(second) {
		t.Fatal("two empty sets produced different artefacts")
	}
	if !bytes.Equal(firstEncoding, secondEncoding) {
		t.Fatal("two empty sets produced different encodings")
	}
	if first.MuHash.Equal(mustNonEmptyMuHash(t)) {
		t.Fatal("the empty set's MuHash equals a non-empty set's")
	}
}

func mustNonEmptyMuHash(t *testing.T) *externalapi.DomainHash {
	t.Helper()
	artifact, _ := buildOrFail(t, testSet())
	return artifact.MuHash
}
