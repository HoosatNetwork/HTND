package pebble

import (
	"bytes"
	"os"
	"testing"

	"github.com/HoosatNetwork/HTND/infrastructure/db/database"
	"github.com/HoosatNetwork/HTND/infrastructure/db/database/ldb"
)

// TestNewPebbleDBLeavesLevelDBDirectoryIntact checks that starting the node's pebble engine on a LevelDB datadir
// (for example a node that ran with --dbtype=leveldb, restarted without it) does not wipe it. NewPebbleDB removes
// a directory whenever pebble reports corruption, which is the intended answer for a corrupted pebble datadir but
// must not be the answer for a valid database written by the other engine.
func TestNewPebbleDBLeavesLevelDBDirectoryIntact(t *testing.T) {
	t.Setenv("HTND_PEBBLE_DISABLE_WAL", "")
	t.Setenv("HTND_TEST_MODE", "")

	dir := t.TempDir()
	key := database.MakeBucket([]byte("bucket")).Key([]byte("key"))
	value := []byte("value")

	levelDB, err := ldb.NewLevelDB(dir, 0)
	if err != nil {
		t.Fatalf("NewLevelDB: %+v", err)
	}
	if err := levelDB.Put(key, value); err != nil {
		t.Fatalf("Put: %+v", err)
	}
	if err := levelDB.Close(); err != nil {
		t.Fatalf("Close: %+v", err)
	}

	pebbleDB, err := NewPebbleDB(dir, 8)
	if err == nil {
		_ = pebbleDB.Close()
	}
	t.Logf("NewPebbleDB on a LevelDB directory returned: %v", err)

	if _, statErr := os.Stat(dir); statErr != nil {
		t.Fatalf("the LevelDB directory is gone after NewPebbleDB: %+v", statErr)
	}
	reopened, err := ldb.NewLevelDB(dir, 0)
	if err != nil {
		t.Fatalf("the LevelDB database no longer opens after NewPebbleDB: %+v", err)
	}
	defer reopened.Close()
	got, err := reopened.Get(key)
	if err != nil {
		t.Fatalf("the LevelDB database lost its data after NewPebbleDB: %+v", err)
	}
	if !bytes.Equal(got, value) {
		t.Fatalf("LevelDB value changed after NewPebbleDB: %q", got)
	}
}
