package pebble

import (
	"testing"

	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database"
)

// TestCloseKeepsWritesWithoutWAL closes a database opened with the write-ahead log disabled
// (HTND_PEBBLE_DISABLE_WAL, or HTND_TEST_MODE) and opens it again. A write acknowledged before a clean
// Close must still be there: with no WAL to replay, it survives only if Close flushed it.
func TestCloseKeepsWritesWithoutWAL(t *testing.T) {
	t.Setenv("HTND_PEBBLE_DISABLE_WAL", "true")
	path := t.TempDir()
	key := database.MakeBucket([]byte("bucket")).Key([]byte("key"))

	db, err := NewPebbleDB(path, 8)
	if err != nil {
		t.Fatalf("NewPebbleDB: %+v", err)
	}
	if err := db.Put(key, []byte("value")); err != nil {
		t.Fatalf("Put: %+v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %+v", err)
	}

	db, err = NewPebbleDB(path, 8)
	if err != nil {
		t.Fatalf("reopening: %+v", err)
	}
	defer db.Close()
	has, err := db.Has(key)
	if err != nil {
		t.Fatalf("Has: %+v", err)
	}
	if !has {
		t.Fatalf("a write acknowledged before a clean Close was lost on reopening")
	}
}
