package pebble

import (
	"bytes"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database"
)

// TestTransactionBatchPutIsVisibleInTransaction pins that values written with BatchPut are read back
// by Get and Has in the same transaction, like values written with Put, including for a key the
// transaction deleted earlier.
func TestTransactionBatchPutIsVisibleInTransaction(t *testing.T) {
	db, err := NewPebbleDB(t.TempDir(), 8)
	if err != nil {
		t.Fatalf("NewPebbleDB: %+v", err)
	}
	defer db.Close()

	bucket := database.MakeBucket([]byte("bucket"))
	fresh := bucket.Key([]byte("fresh"))
	recreated := bucket.Key([]byte("recreated"))
	if err := db.Put(recreated, []byte("old")); err != nil {
		t.Fatalf("Put: %+v", err)
	}

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("Begin: %+v", err)
	}
	defer tx.RollbackUnlessClosed()

	if err := tx.Delete(recreated); err != nil {
		t.Fatalf("Delete: %+v", err)
	}
	if err := tx.BatchPut(map[*database.Key][]byte{fresh: []byte("fresh value"), recreated: []byte("new")}); err != nil {
		t.Fatalf("BatchPut: %+v", err)
	}

	for key, want := range map[*database.Key][]byte{fresh: []byte("fresh value"), recreated: []byte("new")} {
		got, err := tx.Get(key)
		if err != nil {
			t.Fatalf("Get(%s): %+v", key, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("Get(%s) = %q, want %q", key, got, want)
		}
		has, err := tx.Has(key)
		if err != nil || !has {
			t.Fatalf("Has(%s) = %t, %v; want true", key, has, err)
		}
	}
}
