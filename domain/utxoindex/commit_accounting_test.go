package utxoindex

import (
	"os"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/database/binaryserialization"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/utxo"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database/ldb"
)

// TestCommitCountsOnlyWhatChangesTheStoredSet pins that a change the index already reflects leaves the
// per-address UTXO count and the circulating supply alone. After a rebuild, the changes queued behind it
// are applied on top, and the rebuild read virtual after some of them had happened: such a change adds a
// coin already stored or removes one already gone. Counting it again moved the count - even below zero -
// and the supply away from the coins actually stored.
func TestCommitCountsOnlyWhatChangesTheStoredSet(t *testing.T) {
	path, err := os.MkdirTemp("", "utxoindex-commit-accounting")
	if err != nil {
		t.Fatalf("MkdirTemp: %s", err)
	}
	defer os.RemoveAll(path)

	db, err := ldb.NewLevelDB(path, 8)
	if err != nil {
		t.Fatalf("NewLevelDB: %s", err)
	}
	defer db.Close()
	if err := db.Put(circulatingSupplyKey, binaryserialization.SerializeUint64(0)); err != nil {
		t.Fatalf("initializing circulating supply: %s", err)
	}

	store := newUTXOIndexStore(db)
	script, outpoint := testScript(), testOutpoint()

	commit := func(step string, stage func() error) {
		t.Helper()
		if err := stage(); err != nil {
			t.Fatalf("%s: staging: %s", step, err)
		}
		if err := store.commit(); err != nil {
			t.Fatalf("%s: commit: %s", step, err)
		}
	}
	expect := func(step string, wantCount, wantSupply uint64) {
		t.Helper()
		count := uint64(0)
		countBytes, err := db.Get(store.utxoCountKeyForScriptPublicKey(script))
		switch {
		case err == nil:
			count, err = binaryserialization.DeserializeUint64(countBytes)
			if err != nil {
				t.Fatalf("%s: count: %s", step, err)
			}
		case !database.IsNotFoundError(err):
			t.Fatalf("%s: count: %s", step, err)
		}
		supply, err := store.getCirculatingSompiSupply()
		if err != nil {
			t.Fatalf("%s: supply: %s", step, err)
		}
		if count != wantCount || supply != wantSupply {
			t.Fatalf("%s: count %d, supply %d; want count %d, supply %d", step, count, supply, wantCount, wantSupply)
		}
	}

	coin := utxo.NewUTXOEntry(1000, script, false, 100)
	commit("add", func() error { return store.add(script, outpoint, coin) })
	expect("add", 1, 1000)

	commit("replayed add", func() error { return store.add(script, outpoint, coin) })
	expect("replayed add", 1, 1000)

	restamped := utxo.NewUTXOEntry(1500, script, false, 200)
	commit("replacement", func() error {
		if err := store.remove(script, outpoint, coin); err != nil {
			return err
		}
		return store.add(script, outpoint, restamped)
	})
	expect("replacement", 1, 1500)

	commit("remove", func() error { return store.remove(script, outpoint, restamped) })
	expect("remove", 0, 0)

	commit("replayed remove", func() error { return store.remove(script, outpoint, restamped) })
	expect("replayed remove", 0, 0)
}
