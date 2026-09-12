package rpchandlers

import (
	"testing"

	"github.com/HoosatNetwork/HTND/app/rpc/rpccontext"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/utxo"
	"github.com/HoosatNetwork/HTND/domain/utxoindex"
)

// virtualHolding answers as virtual's UTXO set would: an entry for the coins it holds, nil for the rest.
type virtualHolding map[externalapi.DomainOutpoint]externalapi.UTXOEntry

func (v virtualHolding) GetVirtualUTXOEntries(outpoints []*externalapi.DomainOutpoint) ([]externalapi.UTXOEntry, error) {
	entries := make([]externalapi.UTXOEntry, len(outpoints))
	for i, outpoint := range outpoints {
		entries[i] = v[*outpoint]
	}
	return entries, nil
}

func balanceTestOutpoint(id byte) externalapi.DomainOutpoint {
	return externalapi.DomainOutpoint{
		TransactionID: externalapi.DomainTransactionID(*externalapi.NewDomainHashFromByteArray(
			&[externalapi.DomainHashSize]byte{id})),
		Index: 0,
	}
}

// A balance must count only what can actually be spent. A coin the index lists but consensus does not
// hold is not spendable - a transaction built on it is refused for a missing input - so counting it
// tells the wallet it has money it cannot move, and contradicts the coins this same node serves.
func TestBalanceCountsOnlyCoinsConsensusHolds(t *testing.T) {
	script := &externalapi.ScriptPublicKey{Script: []byte{0x51}, Version: 0}
	held, gone, drifted := balanceTestOutpoint(1), balanceTestOutpoint(2), balanceTestOutpoint(3)

	consensus := virtualHolding{
		held:    utxo.NewUTXOEntry(100, script, false, 500),
		drifted: utxo.NewUTXOEntry(300, script, false, 900), // consensus's amount and stamp
	}
	fromIndex := []utxoindex.UTXOPair{
		{Outpoint: held, Entry: utxo.NewUTXOEntry(100, script, false, 500)},
		{Outpoint: gone, Entry: utxo.NewUTXOEntry(200, script, false, 600)},
		{Outpoint: drifted, Entry: utxo.NewUTXOEntry(999, script, false, 700)}, // the index's amount is stale
	}

	if indexOnly := sumUTXOPairs(fromIndex); indexOnly != 1299 {
		t.Fatalf("the fixture should add up to 1299 unfiltered, got %d", indexOnly)
	}

	kept, withheld, err := rpccontext.FilterUTXOPairsAgainstVirtual(consensus, fromIndex)
	if err != nil {
		t.Fatalf("FilterUTXOPairsAgainstVirtual: %+v", err)
	}
	if withheld != 1 {
		t.Errorf("expected the coin consensus does not hold to be withheld, got %d withheld", withheld)
	}
	if balance := sumUTXOPairs(kept); balance != 400 {
		t.Errorf("balance should be 100 + 300, the coins consensus holds at consensus's amounts; got %d", balance)
	}
}
