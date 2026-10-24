package pebble

import (
	"testing"

	"github.com/cockroachdb/pebble/v2"
)

func TestOptionsPinsFormat24(t *testing.T) {
	options := Options(8)
	defer options.Cache.Unref()
	// Use a literal: comparing to FormatNewest would follow a future library upgrade.
	if got := options.FormatMajorVersion; got != 24 {
		t.Fatalf("FormatMajorVersion = %d, want 24", got)
	}
}

func TestOptionsKeepsExistingFormat24(t *testing.T) {
	t.Setenv("HTND_PEBBLE_DISABLE_WAL", "false")
	t.Setenv("HTND_TEST_MODE", "false")
	path := t.TempDir()
	db, err := pebble.Open(path, &pebble.Options{FormatMajorVersion: 24})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Set([]byte("key"), []byte("value"), pebble.Sync); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	options := Options(8)
	defer options.Cache.Unref()
	db, err = pebble.Open(path, options)
	if err != nil {
		t.Fatal(err)
	}
	if got := db.FormatMajorVersion(); got != 24 {
		t.Errorf("existing database format = %d, want 24", got)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopen with the explicit old format to check the persisted marker as well.
	// Pebble does not downgrade a database when a lower format is requested.
	db, err = pebble.Open(path, &pebble.Options{FormatMajorVersion: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if got := db.FormatMajorVersion(); got != 24 {
		t.Errorf("persisted database format = %d, want 24", got)
	}
	value, closer, err := db.Get([]byte("key"))
	if err != nil {
		t.Fatal(err)
	}
	defer closer.Close()
	if string(value) != "value" {
		t.Fatalf("value = %q, want value", value)
	}
}
