package rpccontext

import (
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/utxo"
	"github.com/HoosatNetwork/HTND/domain/utxoindex"
	"github.com/pkg/errors"
)

// virtualHolding answers as virtual's UTXO set would: an entry for the coins it holds, nil for the
// rest. answerCount, when set, makes it return the wrong number of answers; busy makes it report that
// the consensus lock could not be taken.
type virtualHolding struct {
	held        map[externalapi.DomainOutpoint]externalapi.UTXOEntry
	answerCount int
	err         error
	busy        bool
	parents     []*externalapi.DomainHash
}

func (v *virtualHolding) GetVirtualUTXOEntries(outpoints []*externalapi.DomainOutpoint, _ time.Duration) ([]externalapi.UTXOEntry, []*externalapi.DomainHash, bool, error) {
	if v.err != nil {
		return nil, nil, false, v.err
	}
	if v.busy {
		return nil, nil, false, nil
	}
	entries := make([]externalapi.UTXOEntry, len(outpoints))
	for i, outpoint := range outpoints {
		entries[i] = v.held[*outpoint]
	}
	if v.answerCount != 0 {
		return entries[:v.answerCount], v.parents, true, nil
	}
	return entries, v.parents, true, nil
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

	kept, withheld, _, err := FilterUTXOPairsAgainstVirtual(source, pairs, nil)
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

	if kept, withheld, _, err := FilterUTXOPairsAgainstVirtual(&virtualHolding{}, nil, nil); err != nil || len(kept) != 0 || withheld != 0 {
		t.Errorf("no coins in, no coins out: got %v, %d, %v", kept, withheld, err)
	}
	if _, _, _, err := FilterUTXOPairsAgainstVirtual(&virtualHolding{err: errors.New("boom")}, pairs, nil); err == nil {
		t.Error("a consensus failure must be reported, not treated as 'the coin does not exist'")
	}
	if _, _, _, err := FilterUTXOPairsAgainstVirtual(&virtualHolding{answerCount: 0, held: nil}, pairs, nil); err != nil {
		t.Errorf("unexpected error: %+v", err)
	}
	short := &virtualHolding{answerCount: 1}
	if _, _, _, err := FilterUTXOPairsAgainstVirtual(short, append(pairs, utxoindex.UTXOPair{Outpoint: testOutpoint(2), Entry: pairs[0].Entry}), nil); err == nil {
		t.Error("an answer that does not cover every outpoint must be an error, not a silent withholding")
	}
}

// While block processing holds the consensus lock - a pruning point UTXO set update holds it for 15
// seconds to nearly 4 minutes on mainnet - the check cannot run, and the RPC must not wait it out:
// waiting is what turned GetUtxosByAddresses into DeadlineExceeded for clients. The index's coins are
// served as they were before the check existed.
func TestFilterUTXOPairsAgainstVirtualServesIndexWhenConsensusIsBusy(t *testing.T) {
	script := &externalapi.ScriptPublicKey{Script: []byte{0x51}, Version: 0}
	pairs := []utxoindex.UTXOPair{
		{Outpoint: testOutpoint(1), Entry: utxo.NewUTXOEntry(100, script, false, 500)},
		{Outpoint: testOutpoint(2), Entry: utxo.NewUTXOEntry(200, script, false, 600)},
	}

	// It holds nothing: had the check run, both coins would have been withheld.
	kept, withheld, _, err := FilterUTXOPairsAgainstVirtual(&virtualHolding{busy: true}, pairs, nil)
	if err != nil {
		t.Fatalf("a busy consensus is not an error: %+v", err)
	}
	if withheld != 0 || len(kept) != 2 {
		t.Fatalf("a busy consensus must leave the index's answer as it is: kept %d, withheld %d", len(kept), withheld)
	}
	if kept[0].Entry.BlockDAAScore() != 500 || kept[1].Entry.BlockDAAScore() != 600 {
		t.Errorf("unchecked coins must carry the index's own entries, got stamps %d and %d",
			kept[0].Entry.BlockDAAScore(), kept[1].Entry.BlockDAAScore())
	}
}

// A coin virtual has just removed is still listed by an index that has not yet applied that change: the
// index applies consensus's changes after consensus commits them, and on a 200ms network the tip moving
// to a sibling takes a whole coinbase's outputs out of virtual several times a minute. Calling that
// drift sent operators looking for corruption that was not there. A missing coin is drift only when the
// index's last applied change leaves it at the virtual state the lookup saw.
func TestFilterUTXOPairsAgainstVirtualReportsDriftOnlyWhenInStep(t *testing.T) {
	script := &externalapi.ScriptPublicKey{Script: []byte{0x51}, Version: 0}
	oldTip := externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{9})
	newTip := externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{10})
	onlyGone := func() []utxoindex.UTXOPair {
		return []utxoindex.UTXOPair{{Outpoint: testOutpoint(1), Entry: utxo.NewUTXOEntry(100, script, false, 500)}}
	}

	for _, test := range []struct {
		name           string
		virtualParents []*externalapi.DomainHash
		indexParents   []*externalapi.DomainHash
		wantDrifted    bool
	}{
		{name: "index one change behind virtual", virtualParents: []*externalapi.DomainHash{newTip},
			indexParents: []*externalapi.DomainHash{oldTip}, wantDrifted: false},
		{name: "index and virtual at the same state", virtualParents: []*externalapi.DomainHash{newTip},
			indexParents: []*externalapi.DomainHash{newTip}, wantDrifted: true},
		{name: "virtual moved during the lookup", virtualParents: nil,
			indexParents: []*externalapi.DomainHash{newTip}, wantDrifted: false},
		{name: "index state unknown", virtualParents: []*externalapi.DomainHash{newTip},
			indexParents: nil, wantDrifted: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := &virtualHolding{parents: test.virtualParents}
			kept, withheld, drifted, err := FilterUTXOPairsAgainstVirtual(source, onlyGone(), test.indexParents)
			if err != nil {
				t.Fatalf("FilterUTXOPairsAgainstVirtual: %+v", err)
			}
			if withheld != 1 || len(kept) != 0 {
				t.Fatalf("a coin virtual does not hold is withheld whatever the index state: kept %d, withheld %d",
					len(kept), withheld)
			}
			if drifted != test.wantDrifted {
				t.Fatalf("drifted = %t, want %t", drifted, test.wantDrifted)
			}
		})
	}

	held := testOutpoint(2)
	source := &virtualHolding{
		held:    map[externalapi.DomainOutpoint]externalapi.UTXOEntry{held: utxo.NewUTXOEntry(100, script, false, 500)},
		parents: []*externalapi.DomainHash{newTip},
	}
	pairs := []utxoindex.UTXOPair{{Outpoint: held, Entry: utxo.NewUTXOEntry(100, script, false, 500)}}
	if _, _, drifted, err := FilterUTXOPairsAgainstVirtual(source, pairs, []*externalapi.DomainHash{newTip}); err != nil || drifted {
		t.Fatalf("nothing withheld is never drift: drifted=%t err=%+v", drifted, err)
	}
}
