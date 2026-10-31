package pebble

import (
	"strings"
	"testing"
)

func TestPP42JournalAdmissionRejectsDisabledWALBeforeWrites(t *testing.T) {
	t.Setenv("HTND_TEST_MODE", "1")
	db, err := OpenPebbleDB(t.TempDir(), 8)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// The generic capability must remain usable, while the durable worker refuses.
	snapshot, err := db.NewSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if err := snapshot.Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.CheckPruningJournalAdmission(); err == nil || !strings.Contains(err.Error(), "WAL") {
		t.Fatalf("WAL-disabled journal admitted: %v", err)
	}
}
