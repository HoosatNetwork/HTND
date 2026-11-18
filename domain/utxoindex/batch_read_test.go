package utxoindex

import (
	"runtime"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/database/binaryserialization"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/utxo"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database/ldb"
	"github.com/HoosatNetwork/HTND/v2/util/memory"
)

func TestBatchedUTXOReadsSurviveGC(t *testing.T) {
	db, err := ldb.NewLevelDB(t.TempDir(), 8)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Put(circulatingSupplyKey, binaryserialization.SerializeUint64(0)); err != nil {
		t.Fatal(err)
	}
	store := newUTXOIndexStore(db)
	script := &externalapi.ScriptPublicKey{Script: []byte{0x51, 0x21, 0x02}, Version: 1}
	const total = 530
	for i := range total {
		outpoint := &externalapi.DomainOutpoint{Index: uint32(i)}
		entry := utxo.NewUTXOEntry(uint64(i+1), script, i%2 == 0, uint64(i+100))
		if err := store.add(script, outpoint, entry); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.commit(); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name          string
		offset, limit uint32
		want          int
	}{
		{"unlimited", 0, 0, total},
		{"limited", 0, 5, 5},
		{"page", 10, 510, 510},
		{"page remainder", 500, 0, 30},
	} {
		t.Run(test.name, func(t *testing.T) {
			buffer := memory.Malloc[UTXOPair](1)
			var pairs []UTXOPair
			var err error
			if test.name == "unlimited" || test.name == "limited" {
				pairs, buffer, err = store.UTXOs(script, test.limit, buffer)
			} else {
				pairs, buffer, err = store.PaginatedUTXOs(script, test.offset, test.limit, buffer)
			}
			if err != nil {
				memory.Free(buffer)
				t.Fatal(err)
			}
			defer memory.Free(buffer)
			if len(pairs) != test.want {
				t.Fatalf("got %d entries, want %d", len(pairs), test.want)
			}
			pairs = nil
			runtime.GC()
			pairs = buffer.Slice()[:test.want]
			for _, pair := range pairs {
				i := pair.Outpoint.Index
				entry := pair.Entry
				if entry.Amount() != uint64(i+1) || entry.IsCoinbase() != (i%2 == 0) ||
					entry.BlockDAAScore() != uint64(i+100) || !entry.ScriptPublicKey().Equal(script) {
					t.Fatalf("invalid entry for outpoint index %d after GC", i)
				}
				if entry.ScriptPublicKey() != pairs[0].Entry.ScriptPublicKey() {
					t.Fatal("matching scripts were not shared")
				}
			}
			runtime.KeepAlive(buffer)
		})
	}
}
