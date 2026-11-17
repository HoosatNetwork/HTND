package utxoindex

import (
	"os"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/database/binaryserialization"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	consensusutxo "github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/utxo"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database/ldb"
)

// dumpDatabase returns every key and value of the database.
func dumpDatabase(t *testing.T, db database.Database) map[string]string {
	t.Helper()
	cursor, err := db.Cursor(database.MakeBucket(nil))
	if err != nil {
		t.Fatalf("Cursor: %s", err)
	}
	defer cursor.Close()
	content := map[string]string{}
	for ok := cursor.First(); ok; ok = cursor.Next() {
		key, err := cursor.FullKey()
		if err != nil {
			t.Fatalf("FullKey: %s", err)
		}
		value, err := cursor.Value()
		if err != nil {
			t.Fatalf("Value: %s", err)
		}
		content[string(key)] = string(value)
	}
	return content
}

// TestUpdateSkipsChangeListWithoutListeners pins that Update builds the list of UTXO changes only when asked to,
// which is when a client is subscribed to UTXO changes, and that what is written to the index does not depend on it.
func TestUpdateSkipsChangeListWithoutListeners(t *testing.T) {
	newIndex := func() (*UTXOIndex, database.Database) {
		path, err := os.MkdirTemp("", "utxoindex-no-listeners")
		if err != nil {
			t.Fatalf("MkdirTemp: %s", err)
		}
		t.Cleanup(func() { os.RemoveAll(path) })
		db, err := ldb.NewLevelDB(path, 8)
		if err != nil {
			t.Fatalf("NewLevelDB: %s", err)
		}
		t.Cleanup(func() { db.Close() })
		if err := db.Put(circulatingSupplyKey, binaryserialization.SerializeUint64(0)); err != nil {
			t.Fatalf("initializing circulating supply: %s", err)
		}
		return &UTXOIndex{store: newUTXOIndexStore(db)}, db
	}

	script := testScript()
	outpoint := func(index uint32) externalapi.DomainOutpoint {
		return externalapi.DomainOutpoint{
			TransactionID: *externalapi.NewDomainTransactionIDFromByteArray(&[externalapi.DomainHashSize]byte{7}),
			Index:         index,
		}
	}
	collection := func(indexes ...uint32) externalapi.UTXOCollection {
		coins := map[externalapi.DomainOutpoint]externalapi.UTXOEntry{}
		for _, index := range indexes {
			coins[outpoint(index)] = consensusutxo.NewUTXOEntry(1000+uint64(index), script, false, 100)
		}
		return consensusutxo.NewUTXOCollection(coins)
	}
	changeSet := func(toAdd, toRemove externalapi.UTXOCollection, parent byte) *externalapi.VirtualChangeSet {
		diff, err := consensusutxo.NewUTXODiffFromCollections(toAdd, toRemove)
		if err != nil {
			t.Fatalf("NewUTXODiffFromCollections: %s", err)
		}
		return &externalapi.VirtualChangeSet{
			VirtualUTXODiff: diff,
			VirtualParents: []*externalapi.DomainHash{
				externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{parent}),
			},
		}
	}
	changeSets := []*externalapi.VirtualChangeSet{
		changeSet(collection(1, 2), collection(), 1),
		changeSet(collection(3), collection(1), 2),
	}

	withListeners, withListenersDB := newIndex()
	withoutListeners, withoutListenersDB := newIndex()
	for i, cs := range changeSets {
		changes, err := withListeners.Update(cs, func() bool { return true })
		if err != nil {
			t.Fatalf("Update with listeners, change set %d: %s", i, err)
		}
		if changes == nil || len(changes.Added) == 0 {
			t.Fatalf("change set %d: Update with listeners returned no added UTXOs: %+v", i, changes)
		}

		changes, err = withoutListeners.Update(cs, func() bool { return false })
		if err != nil {
			t.Fatalf("Update without listeners, change set %d: %s", i, err)
		}
		if changes != nil {
			t.Fatalf("change set %d: Update without listeners returned a change list: %+v", i, changes)
		}
	}

	want, got := dumpDatabase(t, withListenersDB), dumpDatabase(t, withoutListenersDB)
	if len(want) == 0 {
		t.Fatalf("the index wrote nothing, so the test compares nothing")
	}
	if len(got) != len(want) {
		t.Fatalf("the index without listeners holds %d keys, with listeners %d", len(got), len(want))
	}
	for key, value := range want {
		if got[key] != value {
			t.Fatalf("key %x differs: %x without listeners, %x with them", key, got[key], value)
		}
	}
}

// TestUpdateAsksForListenersWhileLocked pins that Update calls hasListeners while it holds the index lock, so a
// client that subscribes after the call can only read the index once the update is committed.
func TestUpdateAsksForListenersWhileLocked(t *testing.T) {
	db, err := ldb.NewLevelDB(t.TempDir(), 8)
	if err != nil {
		t.Fatalf("NewLevelDB: %s", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Put(circulatingSupplyKey, binaryserialization.SerializeUint64(0)); err != nil {
		t.Fatalf("initializing circulating supply: %s", err)
	}
	index := &UTXOIndex{store: newUTXOIndexStore(db)}

	diff, err := consensusutxo.NewUTXODiffFromCollections(consensusutxo.NewUTXOCollection(nil), consensusutxo.NewUTXOCollection(nil))
	if err != nil {
		t.Fatalf("NewUTXODiffFromCollections: %s", err)
	}
	called := false
	_, err = index.Update(&externalapi.VirtualChangeSet{
		VirtualUTXODiff: diff,
		VirtualParents:  []*externalapi.DomainHash{externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{1})},
	}, func() bool {
		called = true
		if index.mutex.TryRLock() {
			index.mutex.RUnlock()
			t.Error("hasListeners was called without the index lock held")
		}
		return false
	})
	if err != nil {
		t.Fatalf("Update: %s", err)
	}
	if !called {
		t.Fatal("hasListeners was not called")
	}
}
