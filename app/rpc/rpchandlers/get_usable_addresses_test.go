package rpchandlers

import (
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/utxo"
	"github.com/HoosatNetwork/HTND/v2/domain/utxoindex"
	"github.com/HoosatNetwork/HTND/v2/util/memory"
)

// indexListing answers as the UTXO index would for one address, and records the limit of each read.
type indexListing struct {
	pairs  []utxoindex.UTXOPair
	limits []uint32
}

func (l *indexListing) UTXOs(_ *externalapi.ScriptPublicKey, limit uint32, buffer *memory.Block[utxoindex.UTXOPair]) (
	[]utxoindex.UTXOPair, *memory.Block[utxoindex.UTXOPair], []*externalapi.DomainHash, error,
) {
	l.limits = append(l.limits, limit)
	pairs := l.pairs
	if limit > 0 && int(limit) < len(pairs) {
		pairs = pairs[:limit]
	}
	return append([]utxoindex.UTXOPair(nil), pairs...), buffer, nil, nil
}

func usabilityTestOutpoint(i int) externalapi.DomainOutpoint {
	return externalapi.DomainOutpoint{
		TransactionID: externalapi.DomainTransactionID(*externalapi.NewDomainHashFromByteArray(
			&[externalapi.DomainHashSize]byte{byte(i), byte(i >> 8)})),
		Index: 0,
	}
}

// usabilityFixture lists coinCount coins for the address, of which consensus holds only those in held.
func usabilityFixture(coinCount int, held ...int) (*indexListing, virtualHolding) {
	script := &externalapi.ScriptPublicKey{Script: []byte{0x51}, Version: 0}
	listing := &indexListing{}
	consensus := virtualHolding{}
	for i := 0; i < coinCount; i++ {
		outpoint := usabilityTestOutpoint(i)
		listing.pairs = append(listing.pairs, utxoindex.UTXOPair{Outpoint: outpoint, Entry: utxo.NewUTXOEntry(1, script, false, 1)})
	}
	for _, i := range held {
		consensus[usabilityTestOutpoint(i)] = utxo.NewUTXOEntry(1, script, false, 1)
	}
	return listing, consensus
}

// One spendable coin answers the question, so an address holding many coins must not have all of them
// read and checked - and an address whose probed coins are all stale must still be found usable when
// a later coin is spendable, giving the same answer as checking every coin.
func TestHoldsSpendableCoinProbesBeforeReadingEverything(t *testing.T) {
	script := &externalapi.ScriptPublicKey{Script: []byte{0x51}, Version: 0}
	tests := []struct {
		name       string
		coinCount  int
		held       []int
		wantUsable bool
		wantLimits []uint32
	}{
		{name: "first coin spendable", coinCount: 5000, held: []int{0}, wantUsable: true,
			wantLimits: []uint32{1}},
		{name: "first coin stale, spendable coin within the second probe", coinCount: 5000, held: []int{3},
			wantUsable: true, wantLimits: []uint32{1, 32}},
		{name: "both probes stale, spendable coin past them", coinCount: 100, held: []int{42},
			wantUsable: true, wantLimits: []uint32{1, 32, 0}},
		{name: "one coin, stale", coinCount: 1, wantUsable: false, wantLimits: []uint32{1, 32}},
		{name: "fewer coins than the second probe, all stale", coinCount: 5, wantUsable: false,
			wantLimits: []uint32{1, 32}},
		{name: "exactly the second probe, all stale", coinCount: 32, wantUsable: false,
			wantLimits: []uint32{1, 32, 0}},
		{name: "no coins", coinCount: 0, wantUsable: false, wantLimits: []uint32{1}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			listing, consensus := usabilityFixture(test.coinCount, test.held...)
			usable, err := holdsSpendableCoin(listing, consensus, script, "hoosat:test")
			if err != nil {
				t.Fatalf("holdsSpendableCoin: %+v", err)
			}
			if usable != test.wantUsable {
				t.Errorf("usable = %t, want %t", usable, test.wantUsable)
			}
			if len(listing.limits) != len(test.wantLimits) {
				t.Fatalf("index reads with limits %v, want %v", listing.limits, test.wantLimits)
			}
			for i := range listing.limits {
				if listing.limits[i] != test.wantLimits[i] {
					t.Fatalf("index reads with limits %v, want %v", listing.limits, test.wantLimits)
				}
			}
		})
	}
}
