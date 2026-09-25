package utxo

import (
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/multiset"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/subnetworks"
)

func coinbaseTransaction(value uint64) *externalapi.DomainTransaction {
	return &externalapi.DomainTransaction{
		SubnetworkID: subnetworks.SubnetworkIDCoinbase,
		Outputs: []*externalapi.DomainTransactionOutput{{
			Value:           value,
			ScriptPublicKey: &externalapi.ScriptPublicKey{Script: []byte{0x51}, Version: 0},
		}},
	}
}

func acceptanceOf(transactions ...*externalapi.DomainTransaction) externalapi.AcceptanceData {
	data := make([]*externalapi.TransactionAcceptanceData, 0, len(transactions))
	for _, transaction := range transactions {
		data = append(data, &externalapi.TransactionAcceptanceData{Transaction: transaction, IsAccepted: true})
	}
	return externalapi.AcceptanceData{{
		BlockHash:                 externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{7}),
		TransactionAcceptanceData: data,
	}}
}

// TestMultisetDoesNotHashADuplicateCoinbaseTwice is the divergence that makes a correct block fail
// its UTXO commitment with no other symptom.
//
// Two blocks with byte-identical coinbases share a transaction ID, and each can be accepted by a
// different merging block - on a live mainnet survey that happened to 8,174 coinbase transactions.
// The UTXO diff refuses the second add, because a set holds an outpoint once. A MuHash Add is not
// idempotent, so a multiset that adds it again is no longer the hash of any set, and the block's
// commitment stops matching its header while the block itself is entirely correct.
func TestMultisetDoesNotHashADuplicateCoinbaseTwice(t *testing.T) {
	const daaScore = 500
	coinbase := coinbaseTransaction(100)
	outpoint := &externalapi.DomainOutpoint{TransactionID: *consensushashing.TransactionID(coinbase), Index: 0}

	// The set this block starts from does not hold the coin yet - it is created by this very
	// acceptance data, and presented twice because two merge-set blocks carried the same coinbase.
	parentSet, err := NewUTXODiffFromCollections(
		NewUTXOCollection(map[externalapi.DomainOutpoint]externalapi.UTXOEntry{}),
		NewUTXOCollection(map[externalapi.DomainOutpoint]externalapi.UTXOEntry{}))
	if err != nil {
		t.Fatalf("NewUTXODiffFromCollections: %+v", err)
	}
	_ = outpoint

	once := multiset.New()
	if err := ApplyAcceptanceDataToMultiset(once, acceptanceOf(coinbase), daaScore, parentSet, nil); err != nil {
		t.Fatalf("ApplyAcceptanceDataToMultiset: %+v", err)
	}

	// The same coin presented twice by acceptance data must produce the same multiset, because it is
	// the same set either way.
	twice := multiset.New()
	if err := ApplyAcceptanceDataToMultiset(twice, acceptanceOf(coinbase, coinbase), daaScore, parentSet, nil); err != nil {
		t.Fatalf("ApplyAcceptanceDataToMultiset: %+v", err)
	}

	if !once.Hash().Equal(twice.Hash()) {
		t.Errorf("a coin the diff holds once must be hashed once however many times acceptance data "+
			"presents it\n  once:  %s\n  twice: %s", once.Hash(), twice.Hash())
	}
}

// Deduplication within one replay does not depend on knowing the starting set: the same outpoint
// twice in one acceptance data is one coin either way.
func TestMultisetDeduplicatesWithinAReplayWithoutTheParentSet(t *testing.T) {
	const daaScore = 500
	coinbase := coinbaseTransaction(100)

	once := multiset.New()
	if err := ApplyAcceptanceDataToMultiset(once, acceptanceOf(coinbase), daaScore, nil, nil); err != nil {
		t.Fatalf("ApplyAcceptanceDataToMultiset: %+v", err)
	}
	twice := multiset.New()
	if err := ApplyAcceptanceDataToMultiset(twice, acceptanceOf(coinbase, coinbase), daaScore, nil, nil); err != nil {
		t.Fatalf("ApplyAcceptanceDataToMultiset: %+v", err)
	}
	// Within-replay deduplication does not need the parent set, so the two are equal even here.
	if !once.Hash().Equal(twice.Hash()) {
		t.Error("the same outpoint presented twice in one replay must still be hashed once - a set " +
			"holds it once regardless of what the caller supplied")
	}
}

// A coin created and spent inside the same acceptance data is absent from the diff for a completely
// different reason - it nets to nothing - and must still be added and removed, or the multiset loses
// a removal it owes.
func TestMultisetStillAppliesACoinCreatedAndSpentInTheSameBlock(t *testing.T) {
	const daaScore = 500
	creator := coinbaseTransaction(100)
	created := &externalapi.DomainOutpoint{TransactionID: *consensushashing.TransactionID(creator), Index: 0}
	spender := &externalapi.DomainTransaction{
		SubnetworkID: subnetworks.SubnetworkIDNative,
		Inputs: []*externalapi.DomainTransactionInput{{
			PreviousOutpoint: *created,
			UTXOEntry:        NewUTXOEntry(100, creator.Outputs[0].ScriptPublicKey, true, daaScore),
		}},
	}

	empty, err := NewUTXODiffFromCollections(
		NewUTXOCollection(map[externalapi.DomainOutpoint]externalapi.UTXOEntry{}),
		NewUTXOCollection(map[externalapi.DomainOutpoint]externalapi.UTXOEntry{}))
	if err != nil {
		t.Fatalf("NewUTXODiffFromCollections: %+v", err)
	}

	netted := multiset.New()
	if err := ApplyAcceptanceDataToMultiset(netted, acceptanceOf(creator, spender), daaScore, empty, nil); err != nil {
		t.Fatalf("ApplyAcceptanceDataToMultiset: %+v", err)
	}
	if !netted.Hash().Equal(multiset.New().Hash()) {
		t.Error("a coin created and spent in the same acceptance data must net to nothing; skipping " +
			"its add while still applying its remove would leave the multiset short a coin")
	}
}

// TestMultisetSkipsACoinTheSetAlreadyHeld covers the case measured on mainnet: a byte-identical
// coinbase accepted by one chain block and then accepted again by a later one. The coin is already
// in the set the later block starts from, the diff refuses to add it a second time, and the multiset
// must refuse too or it stops being the hash of that set.
func TestMultisetSkipsACoinTheSetAlreadyHeld(t *testing.T) {
	const daaScore = 500
	coinbase := coinbaseTransaction(100)
	outpoint := &externalapi.DomainOutpoint{TransactionID: *consensushashing.TransactionID(coinbase), Index: 0}

	alreadyHeld, err := NewUTXODiffFromCollections(
		NewUTXOCollection(map[externalapi.DomainOutpoint]externalapi.UTXOEntry{
			*outpoint: NewUTXOEntry(100, coinbase.Outputs[0].ScriptPublicKey, true, daaScore),
		}),
		NewUTXOCollection(map[externalapi.DomainOutpoint]externalapi.UTXOEntry{}))
	if err != nil {
		t.Fatalf("NewUTXODiffFromCollections: %+v", err)
	}

	ms := multiset.New()
	if err := ApplyAcceptanceDataToMultiset(ms, acceptanceOf(coinbase), daaScore, alreadyHeld, nil); err != nil {
		t.Fatalf("ApplyAcceptanceDataToMultiset: %+v", err)
	}
	if !ms.Hash().Equal(multiset.New().Hash()) {
		t.Error("a coin the starting set already holds must not be hashed again; the diff refuses the " +
			"second add, and a multiset that does not refuse it is no longer the hash of that set")
	}
}

// TestMultisetSkipsACoinHeldOnlyInTheBase covers the tip-child shape that ToAdd-only dedup missed.
//
// After an earlier chain block accepted a coinbase and became the selected tip, the coin sits in
// virtual. restorePastUTXO of that tip returns a diff relative to virtual in which the coin appears
// in neither ToAdd nor ToRemove (it is in both the past and virtual). A later block that accepts the
// same byte-identical coinbase again must not hash it: the set already holds it. Without baseUTXO,
// ApplyAcceptanceDataToMultiset only consulted ToAdd and hashed again — the commitment of a correct
// relayed block then depended on whether this node's diff path had put the coin in ToAdd or only in
// virtual.
func TestMultisetSkipsACoinHeldOnlyInTheBase(t *testing.T) {
	const daaScore = 500
	coinbase := coinbaseTransaction(100)
	outpoint := &externalapi.DomainOutpoint{TransactionID: *consensushashing.TransactionID(coinbase), Index: 0}

	// Empty diff: the coin is not in ToAdd/ToRemove, exactly as restorePastUTXO returns when the
	// coin is already in virtual and also in the selected parent's past.
	emptyDiff, err := NewUTXODiffFromCollections(
		NewUTXOCollection(map[externalapi.DomainOutpoint]externalapi.UTXOEntry{}),
		NewUTXOCollection(map[externalapi.DomainOutpoint]externalapi.UTXOEntry{}))
	if err != nil {
		t.Fatalf("NewUTXODiffFromCollections: %+v", err)
	}

	entry := NewUTXOEntry(100, coinbase.Outputs[0].ScriptPublicKey, true, daaScore)
	baseUTXO := func(o *externalapi.DomainOutpoint) (externalapi.UTXOEntry, bool, error) {
		if o.Equal(outpoint) {
			return entry, true, nil
		}
		return nil, false, nil
	}

	ms := multiset.New()
	if err := ApplyAcceptanceDataToMultiset(ms, acceptanceOf(coinbase), daaScore, emptyDiff, baseUTXO); err != nil {
		t.Fatalf("ApplyAcceptanceDataToMultiset: %+v", err)
	}
	if !ms.Hash().Equal(multiset.New().Hash()) {
		t.Error("a coin the base (virtual) already holds must not be hashed again when the past diff " +
			"has no ToAdd entry for it; that is the tip-child representation of an already-held coin")
	}

	// Control: without baseUTXO the bug hashes again (documents the old behaviour).
	buggy := multiset.New()
	if err := ApplyAcceptanceDataToMultiset(buggy, acceptanceOf(coinbase), daaScore, emptyDiff, nil); err != nil {
		t.Fatalf("ApplyAcceptanceDataToMultiset: %+v", err)
	}
	if buggy.Hash().Equal(multiset.New().Hash()) {
		t.Fatal("control failed: without baseUTXO the empty-diff already-held case should still hash")
	}
}

// TestMultisetRestampsWhenDAADiffers: addEntry keeps the incoming BlockDAAScore when the same coin
// is already held under a different stamp. The multiset must Remove(old)+Add(new) or the table and
// the commitment disagree (HTN-005-shaped drift).
func TestMultisetRestampsWhenDAADiffers(t *testing.T) {
	const oldDAA, newDAA = uint64(400), uint64(500)
	coinbase := coinbaseTransaction(100)
	outpoint := &externalapi.DomainOutpoint{TransactionID: *consensushashing.TransactionID(coinbase), Index: 0}
	script := coinbase.Outputs[0].ScriptPublicKey
	oldEntry := NewUTXOEntry(100, script, true, oldDAA)
	newEntry := NewUTXOEntry(100, script, true, newDAA)

	emptyDiff, err := NewUTXODiffFromCollections(
		NewUTXOCollection(map[externalapi.DomainOutpoint]externalapi.UTXOEntry{}),
		NewUTXOCollection(map[externalapi.DomainOutpoint]externalapi.UTXOEntry{}))
	if err != nil {
		t.Fatalf("NewUTXODiffFromCollections: %+v", err)
	}

	baseUTXO := func(o *externalapi.DomainOutpoint) (externalapi.UTXOEntry, bool, error) {
		if o.Equal(outpoint) {
			return oldEntry, true, nil
		}
		return nil, false, nil
	}

	// Expected: start empty, Add(old), Remove(old), Add(new) ≡ just Add(new).
	expected := multiset.New()
	expected.Add(mustSerialize(t, newEntry, outpoint))

	got := multiset.New()
	got.Add(mustSerialize(t, oldEntry, outpoint))
	if err := ApplyAcceptanceDataToMultiset(got, acceptanceOf(coinbase), newDAA, emptyDiff, baseUTXO); err != nil {
		t.Fatalf("ApplyAcceptanceDataToMultiset: %+v", err)
	}
	if !got.Hash().Equal(expected.Hash()) {
		t.Errorf("restamp must leave the multiset equal to hashing the new stamp once\n  got:      %s\n  expected: %s",
			got.Hash(), expected.Hash())
	}

	// Same via ToAdd (past holds old stamp, not virtual).
	pastDiff, err := NewUTXODiffFromCollections(
		NewUTXOCollection(map[externalapi.DomainOutpoint]externalapi.UTXOEntry{
			*outpoint: oldEntry,
		}),
		NewUTXOCollection(map[externalapi.DomainOutpoint]externalapi.UTXOEntry{}))
	if err != nil {
		t.Fatalf("NewUTXODiffFromCollections: %+v", err)
	}
	got2 := multiset.New()
	got2.Add(mustSerialize(t, oldEntry, outpoint))
	if err := ApplyAcceptanceDataToMultiset(got2, acceptanceOf(coinbase), newDAA, pastDiff, nil); err != nil {
		t.Fatalf("ApplyAcceptanceDataToMultiset: %+v", err)
	}
	if !got2.Hash().Equal(expected.Hash()) {
		t.Errorf("ToAdd restamp must match base restamp\n  got:      %s\n  expected: %s",
			got2.Hash(), expected.Hash())
	}
}

func mustSerialize(t *testing.T, entry externalapi.UTXOEntry, outpoint *externalapi.DomainOutpoint) []byte {
	t.Helper()
	b, err := SerializeUTXO(entry, outpoint)
	if err != nil {
		t.Fatalf("SerializeUTXO: %+v", err)
	}
	return b
}
