package rpccontext

import (
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/utxo"
	"github.com/HoosatNetwork/HTND/domain/utxoindex"
	"github.com/pkg/errors"
)

// virtualHolding answers as virtual's UTXO set would: an entry for the coins it holds, nil for the
// rest. answerCount, when set, makes it return the wrong number of answers.
type virtualHolding struct {
	held        map[externalapi.DomainOutpoint]externalapi.UTXOEntry
	answerCount int
	err         error
}

func (v *virtualHolding) GetVirtualUTXOEntries(outpoints []*externalapi.DomainOutpoint) ([]externalapi.UTXOEntry, error) {
	if v.err != nil {
		return nil, v.err
	}
	entries := make([]externalapi.UTXOEntry, len(outpoints))
	for i, outpoint := range outpoints {
		entries[i] = v.held[*outpoint]
	}
	if v.answerCount != 0 {
		return entries[:v.answerCount], nil
	}
	return entries, nil
}

func testOutpoint(id byte) externalapi.DomainOutpoint {
	return externalapi.DomainOutpoint{
		TransactionID: externalapi.DomainTransactionID(*externalapi.NewDomainHashFromByteArray(
			&[externalapi.DomainHashSize]byte{id})),
		Index: 0,
	}
}

// A coin the index lists but virtual does not hold must not reach a spender: a wallet building on it
// produces a transaction every node refuses for a missing input. And a coin virtual does hold must be
// described as virtual has it - a BlockDAAScore changes when a different block takes over merging the
// coin, and the index can be carrying the older one.
func TestFilterUTXOPairsAgainstVirtual(t *testing.T) {
	script := &externalapi.ScriptPublicKey{Script: []byte{0x51}, Version: 0}
	held, gone, drifted := testOutpoint(1), testOutpoint(2), testOutpoint(3)

	source := &virtualHolding{held: map[externalapi.DomainOutpoint]externalapi.UTXOEntry{
		held:    utxo.NewUTXOEntry(100, script, false, 500),
		drifted: utxo.NewUTXOEntry(300, script, false, 900), // virtual's stamp, not the index's
	}}
	pairs := []utxoindex.UTXOPair{
		{Outpoint: held, Entry: utxo.NewUTXOEntry(100, script, false, 500)},
		{Outpoint: gone, Entry: utxo.NewUTXOEntry(200, script, false, 600)},
		{Outpoint: drifted, Entry: utxo.NewUTXOEntry(300, script, false, 700)},
	}

	kept, withheld, err := FilterUTXOPairsAgainstVirtual(source, pairs)
	if err != nil {
		t.Fatalf("FilterUTXOPairsAgainstVirtual: %+v", err)
	}
	if withheld != 1 {
		t.Errorf("expected 1 coin withheld, got %d", withheld)
	}
	if len(kept) != 2 {
		t.Fatalf("expected 2 coins served, got %d", len(kept))
	}
	if !kept[0].Outpoint.Equal(&held) || !kept[1].Outpoint.Equal(&drifted) {
		t.Fatalf("the surviving coins, in order, should be the two virtual holds: got %v", kept)
	}
	if kept[1].Entry.BlockDAAScore() != 900 {
		t.Errorf("a served coin must carry virtual's entry: stamp is %d, virtual holds it at 900",
			kept[1].Entry.BlockDAAScore())
	}
}

func TestFilterUTXOPairsAgainstVirtualEdgeCases(t *testing.T) {
	script := &externalapi.ScriptPublicKey{Script: []byte{0x51}, Version: 0}
	pairs := []utxoindex.UTXOPair{{Outpoint: testOutpoint(1), Entry: utxo.NewUTXOEntry(100, script, false, 500)}}

	if kept, withheld, err := FilterUTXOPairsAgainstVirtual(&virtualHolding{}, nil); err != nil || len(kept) != 0 || withheld != 0 {
		t.Errorf("no coins in, no coins out: got %v, %d, %v", kept, withheld, err)
	}
	if _, _, err := FilterUTXOPairsAgainstVirtual(&virtualHolding{err: errors.New("boom")}, pairs); err == nil {
		t.Error("a consensus failure must be reported, not treated as 'the coin does not exist'")
	}
	if _, _, err := FilterUTXOPairsAgainstVirtual(&virtualHolding{answerCount: 0, held: nil}, pairs); err != nil {
		t.Errorf("unexpected error: %+v", err)
	}
	short := &virtualHolding{answerCount: 1}
	if _, _, err := FilterUTXOPairsAgainstVirtual(short, append(pairs, utxoindex.UTXOPair{Outpoint: testOutpoint(2), Entry: pairs[0].Entry})); err == nil {
		t.Error("an answer that does not cover every outpoint must be an error, not a silent withholding")
	}
}
