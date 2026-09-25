package pebble

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database"
	crdberrors "github.com/cockroachdb/errors"
	"github.com/cockroachdb/pebble/v2"
)

// corruptedPebbleDir creates a pebble database with synced data in its WAL, closes it, and zeroes four
// bytes early in the newest WAL file, the way pebble's own TestWALCorruption does. A zeroed chunk is
// reported as ErrCorruption on the next open (a damaged checksum near the tail would instead be
// tolerated as an unclean end).
func corruptedPebbleDir(t *testing.T) string {
	t.Setenv("HTND_PEBBLE_DISABLE_WAL", "")
	t.Setenv("HTND_TEST_MODE", "")

	dir := t.TempDir()
	db, err := OpenPebbleDB(dir, 8)
	if err != nil {
		t.Fatalf("OpenPebbleDB: %+v", err)
	}
	// Rotate to a fresh WAL first, so the newest WAL holds the synced records written below.
	if err := db.db.Flush(); err != nil {
		t.Fatalf("Flush: %+v", err)
	}
	bucket := database.MakeBucket([]byte("bucket"))
	value := bytes.Repeat([]byte{'a'}, 4096)
	for i := 0; i < 32; i++ {
		// Synced writes, as in pebble's own WAL corruption tests, so the WAL records sync offsets and a
		// damaged record is reported as corruption rather than tolerated as an unclean tail.
		if err := db.db.Set(bucket.Key([]byte{byte(i >> 8), byte(i)}).Bytes(), value, pebble.Sync); err != nil {
			t.Fatalf("Set: %+v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %+v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %+v", err)
	}
	var newestWAL string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".log") && entry.Name() > newestWAL {
			newestWAL = entry.Name()
		}
	}
	if newestWAL == "" {
		t.Fatalf("no WAL file found in %s", dir)
	}
	walPath := filepath.Join(dir, newestWAL)
	walBytes, err := os.ReadFile(walPath)
	if err != nil {
		t.Fatalf("ReadFile: %+v", err)
	}
	const corruptOffset = 100
	if len(walBytes) <= corruptOffset+4 {
		t.Fatalf("WAL %s too small to corrupt (%d bytes)", walPath, len(walBytes))
	}
	copy(walBytes[corruptOffset:], []byte{0, 0, 0, 0})
	if err := os.WriteFile(walPath, walBytes, 0o600); err != nil {
		t.Fatalf("WriteFile: %+v", err)
	}
	return dir
}

// TestOpenPebbleDBKeepsCorruptedDirectory pins that the open used by offline tools reports corruption
// and leaves the directory alone. The tools used NewPebbleDB, which deletes a corrupted directory, so
// inspecting a damaged datadir copy wiped the copy.
func TestOpenPebbleDBKeepsCorruptedDirectory(t *testing.T) {
	dir := corruptedPebbleDir(t)

	db, err := OpenPebbleDB(dir, 8)
	if err == nil {
		_ = db.Close()
		t.Fatalf("expected opening a corrupted database to fail")
	}
	if !crdberrors.Is(err, pebble.ErrCorruption) {
		t.Fatalf("expected pebble.ErrCorruption, got %+v", err)
	}
	entries, statErr := os.ReadDir(dir)
	if statErr != nil || len(entries) == 0 {
		t.Fatalf("the corrupted database directory should be left intact (entries: %d, err: %v)", len(entries), statErr)
	}
}

// TestNewPebbleDBRecreatesCorruptedDirectory pins the node's behaviour, which the maintainer chose to
// keep: pebble has no repair API, so a corrupted datadir is replaced by a fresh database. The corruption
// check used the standard errors.Is, which does not follow pebble's cockroachdb/errors marks, so a
// corrupted datadir used to fail to open instead.
func TestNewPebbleDBRecreatesCorruptedDirectory(t *testing.T) {
	dir := corruptedPebbleDir(t)

	db, err := NewPebbleDB(dir, 8)
	if err != nil {
		t.Fatalf("NewPebbleDB should recreate a corrupted database: %+v", err)
	}
	defer db.Close()

	has, err := db.Has(database.MakeBucket([]byte("bucket")).Key([]byte{0, 1}))
	if err != nil {
		t.Fatalf("Has: %+v", err)
	}
	if has {
		t.Fatalf("expected a fresh database without the corrupted data")
	}
}
