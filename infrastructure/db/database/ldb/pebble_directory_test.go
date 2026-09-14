package ldb

import (
	"bytes"
	"testing"

	"github.com/HoosatNetwork/HTND/infrastructure/db/database"
	"github.com/HoosatNetwork/HTND/infrastructure/db/database/pebble"
)

// TestNewLevelDBLeavesPebbleDirectoryIntact pins that opening a pebble datadir with the LevelDB engine is refused
// and leaves the directory untouched. NewLevelDB answered goleveldb's corruption error with leveldb.RecoverFile,
// which rebuilds a LevelDB manifest in place, so pointing ldbtool (or --dbtype=leveldb) at a pebble datadir -
// even as a copy source - could destroy it.
func TestNewLevelDBLeavesPebbleDirectoryIntact(t *testing.T) {
	t.Setenv("HTND_PEBBLE_DISABLE_WAL", "")
	t.Setenv("HTND_TEST_MODE", "")

	dir := t.TempDir()
	key := database.MakeBucket([]byte("bucket")).Key([]byte("key"))
	value := []byte("value")

	pebbleDB, err := pebble.OpenPebbleDB(dir, 8)
	if err != nil {
		t.Fatalf("OpenPebbleDB: %+v", err)
	}
	if err := pebbleDB.Put(key, value); err != nil {
		t.Fatalf("Put: %+v", err)
	}
	if err := pebbleDB.Close(); err != nil {
		t.Fatalf("Close: %+v", err)
	}

	levelDB, err := NewLevelDB(dir, 0)
	if err == nil {
		_ = levelDB.Close()
		t.Errorf("expected opening a pebble directory with LevelDB to be refused")
	}

	reopened, err := pebble.OpenPebbleDB(dir, 8)
	if err != nil {
		t.Fatalf("the pebble database no longer opens after the LevelDB attempt: %+v", err)
	}
	defer reopened.Close()
	got, err := reopened.Get(key)
	if err != nil {
		t.Fatalf("the pebble database lost its data after the LevelDB attempt: %+v", err)
	}
	if !bytes.Equal(got, value) {
		t.Fatalf("pebble value changed after the LevelDB attempt: %q", got)
	}
}
