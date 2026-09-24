package utxo

import (
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/multiset"
)

// TestMultisetSkipsAnInputTheSetDoesNotHold pins the multiset side of a transaction accepted despite
// spending a coin the set does not hold. Its absent input has no UTXO entry; serializing it panicked
// the node on a submitted block. The diff records no spend for that input, so the multiset must not
// either, and must still spend the input it does hold and create every output.
func TestMultisetSkipsAnInputTheSetDoesNotHold(t *testing.T) {
	_, _, _, heldOutpoint, missing, _, heldEntry, _, _, _ := testFixtures()
	const daaScore = 9
	script := &externalapi.ScriptPublicKey{Script: []byte{0x51}, Version: 0}
	transaction := &externalapi.DomainTransaction{
		Inputs: []*externalapi.DomainTransactionInput{
			{PreviousOutpoint: *heldOutpoint, UTXOEntry: heldEntry},
			{PreviousOutpoint: *missing, UTXOEntry: nil},
		},
		Outputs: []*externalapi.DomainTransactionOutput{{Value: 40, ScriptPublicKey: script}},
		Payload: []byte{},
	}

	start := multiset.New()
	serializedHeld, err := SerializeUTXO(heldEntry, heldOutpoint)
	if err != nil {
		t.Fatalf("SerializeUTXO: %+v", err)
	}
	start.Add(serializedHeld)

	got := start.Clone()
	if err := ApplyAcceptanceDataToMultiset(got, acceptanceOf(transaction), daaScore, NewUTXODiff(), nil); err != nil {
		t.Fatalf("ApplyAcceptanceDataToMultiset: %+v", err)
	}

	want := multiset.New()
	created := externalapi.NewDomainOutpoint(consensushashing.TransactionID(transaction), 0)
	serializedCreated, err := SerializeUTXO(NewUTXOEntry(40, script, false, AcceptedUTXOBlockDAAScore(daaScore)), created)
	if err != nil {
		t.Fatalf("SerializeUTXO: %+v", err)
	}
	want.Add(serializedCreated)
	if !got.Hash().Equal(want.Hash()) {
		t.Fatalf("multiset = %s, want the held input spent and the output created (%s)", got.Hash(), want.Hash())
	}

	if err := RemoveAcceptanceDataFromMultiset(got, acceptanceOf(transaction), daaScore); err != nil {
		t.Fatalf("RemoveAcceptanceDataFromMultiset: %+v", err)
	}
	if !got.Hash().Equal(start.Hash()) {
		t.Fatalf("removing the acceptance data did not restore the starting multiset")
	}
}

// TestDiffSkipsAnInputTheSetDoesNotHold is the diff side of the same transaction. The pruning manager
// replays chain acceptance data into a diff when the pruning point moves; the absent input's nil entry
// panicked removeEntry there. It must spend the held input, record nothing for the absent one, and
// create every output - the same set the multiset test above commits to.
func TestDiffSkipsAnInputTheSetDoesNotHold(t *testing.T) {
	_, _, _, heldOutpoint, missing, _, heldEntry, _, _, _ := testFixtures()
	const daaScore = 9
	script := &externalapi.ScriptPublicKey{Script: []byte{0x51}, Version: 0}
	transaction := &externalapi.DomainTransaction{
		Inputs: []*externalapi.DomainTransactionInput{
			{PreviousOutpoint: *heldOutpoint, UTXOEntry: heldEntry},
			{PreviousOutpoint: *missing, UTXOEntry: nil},
		},
		Outputs: []*externalapi.DomainTransactionOutput{{Value: 40, ScriptPublicKey: script}},
		Payload: []byte{},
	}

	diff := NewMutableUTXODiff()
	if err := ApplyAcceptanceDataToDiff(diff, acceptanceOf(transaction), daaScore); err != nil {
		t.Fatalf("ApplyAcceptanceDataToDiff: %+v", err)
	}

	toRemove := diff.ToRemove()
	if toRemove.Len() != 1 || !toRemove.Contains(heldOutpoint) {
		t.Fatalf("toRemove = %v, want only the held input", toRemove)
	}
	created := externalapi.NewDomainOutpoint(consensushashing.TransactionID(transaction), 0)
	toAdd := diff.ToAdd()
	entry, ok := toAdd.Get(created)
	if toAdd.Len() != 1 || !ok || !entry.Equal(NewUTXOEntry(40, script, false, AcceptedUTXOBlockDAAScore(daaScore))) {
		t.Fatalf("toAdd = %v, want only the created output", toAdd)
	}
}
