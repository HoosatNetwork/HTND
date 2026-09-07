package utxo

import (
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/multiset"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/subnetworks"
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
	if err := ApplyAcceptanceDataToMultiset(once, acceptanceOf(coinbase), daaScore, parentSet); err != nil {
		t.Fatalf("ApplyAcceptanceDataToMultiset: %+v", err)
	}

	// The same coin presented twice by acceptance data must produce the same multiset, because it is
	// the same set either way.
	twice := multiset.New()
	if err := ApplyAcceptanceDataToMultiset(twice, acceptanceOf(coinbase, coinbase), daaScore, parentSet); err != nil {
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
	if err := ApplyAcceptanceDataToMultiset(once, acceptanceOf(coinbase), daaScore, nil); err != nil {
		t.Fatalf("ApplyAcceptanceDataToMultiset: %+v", err)
	}
	twice := multiset.New()
	if err := ApplyAcceptanceDataToMultiset(twice, acceptanceOf(coinbase, coinbase), daaScore, nil); err != nil {
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
	if err := ApplyAcceptanceDataToMultiset(netted, acceptanceOf(creator, spender), daaScore, empty); err != nil {
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
	if err := ApplyAcceptanceDataToMultiset(ms, acceptanceOf(coinbase), daaScore, alreadyHeld); err != nil {
		t.Fatalf("ApplyAcceptanceDataToMultiset: %+v", err)
	}
	if !ms.Hash().Equal(multiset.New().Hash()) {
		t.Error("a coin the starting set already holds must not be hashed again; the diff refuses the " +
			"second add, and a multiset that does not refuse it is no longer the hash of that set")
	}
}
