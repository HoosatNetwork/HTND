package ldb

import (
	"testing"

	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database"
	"github.com/syndtr/goleveldb/leveldb/util"
)

// TestCursorReadsTheStateItWasOpenedAt pins that a cursor reads the bucket as it was when the cursor was opened,
// whatever is written, deleted or flushed afterwards. The pruning point UTXO set check relies on it: it opens its
// cursor under the consensus lock and walks it without the lock while blocks and later pruning point moves write to the
// database. A cursor that saw those writes would hash a mixed set and stop an honest node.
func TestCursorReadsTheStateItWasOpenedAt(t *testing.T) {
	db, err := NewLevelDB(t.TempDir(), 8)
	if err != nil {
		t.Fatalf("NewLevelDB: %+v", err)
	}
	defer db.Close()

	bucket := database.MakeBucket([]byte("bucket"))
	want := map[string]string{"a": "1", "b": "2", "c": "3"}
	for key, value := range want {
		if err := db.Put(bucket.Key([]byte(key)), []byte(value)); err != nil {
			t.Fatalf("Put: %+v", err)
		}
	}
	cursor, err := db.Cursor(bucket)
	if err != nil {
		t.Fatalf("Cursor: %+v", err)
	}
	defer cursor.Close()

	if err := db.Delete(bucket.Key([]byte("a"))); err != nil {
		t.Fatalf("Delete: %+v", err)
	}
	if err := db.Put(bucket.Key([]byte("b")), []byte("changed")); err != nil {
		t.Fatalf("Put: %+v", err)
	}
	if err := db.Put(bucket.Key([]byte("bb")), []byte("added")); err != nil {
		t.Fatalf("Put: %+v", err)
	}
	if err := db.ldb.CompactRange(util.Range{}); err != nil {
		t.Fatalf("CompactRange: %+v", err)
	}

	got := make(map[string]string)
	for ok := cursor.First(); ok; ok = cursor.Next() {
		key, err := cursor.Key()
		if err != nil {
			t.Fatalf("Key: %+v", err)
		}
		value, err := cursor.Value()
		if err != nil {
			t.Fatalf("Value: %+v", err)
		}
		got[string(key.Suffix())] = string(value)
	}
	if len(got) != len(want) {
		t.Fatalf("the cursor read %v, want %v", got, want)
	}
	for key, value := range want {
		if got[key] != value {
			t.Fatalf("the cursor read %v, want %v", got, want)
		}
	}
}
