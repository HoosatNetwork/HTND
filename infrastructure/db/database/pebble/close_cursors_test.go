package pebble

import (
	"testing"

	"github.com/HoosatNetwork/HTND/infrastructure/db/database"
)

// TestCloseClosesAllCursors pins that closing the database closes every cursor still open on it.
// Close used to range over the tracked cursors while each cursor's Close removed itself from that
// same slice, so every other cursor was skipped and left open.
func TestCloseClosesAllCursors(t *testing.T) {
	db, err := NewPebbleDB(t.TempDir(), 8)
	if err != nil {
		t.Fatalf("NewPebbleDB: %+v", err)
	}

	bucket := database.MakeBucket([]byte("bucket"))
	cursors := make([]*DBCursor, 5)
	for i := range cursors {
		cursor, err := db.Cursor(bucket)
		if err != nil {
			t.Fatalf("Cursor: %+v", err)
		}
		cursors[i] = cursor.(*DBCursor)
	}

	if err := db.Close(); err != nil {
		t.Fatalf("Close: %+v", err)
	}

	for i, cursor := range cursors {
		if !cursor.isClosed {
			t.Errorf("cursor %d was left open when the database closed", i)
		}
	}
}
