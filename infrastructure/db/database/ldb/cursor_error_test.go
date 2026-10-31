package ldb

import (
	"errors"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database"
	"github.com/syndtr/goleveldb/leveldb/iterator"
)

// A native iterator reports read failures through Error, not Next. Release may
// discard that error, so the cursor must read it before releasing the iterator.
type readErrorIterator struct {
	iterator.Iterator
	err      error
	failed   bool
	released bool
}

func (it *readErrorIterator) Next() bool {
	it.failed = true
	return false
}

func (it *readErrorIterator) Error() error {
	if it.failed && !it.released {
		return it.err
	}
	return nil
}

func (it *readErrorIterator) Release() {
	it.released = true
	it.Iterator.Release()
}

func TestCursorCloseReturnsIteratorError(t *testing.T) {
	db, err := NewLevelDB(t.TempDir(), 8)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	bucket := database.MakeBucket([]byte("cursor-error"))
	if err := db.Put(bucket.Key([]byte("a")), []byte("value")); err != nil {
		t.Fatal(err)
	}
	cursor, err := db.Cursor(bucket)
	if err != nil {
		t.Fatal(err)
	}
	c := cursor.(*LevelDBCursor)
	readErr := errors.New("injected iterator read error")
	it := &readErrorIterator{Iterator: c.ldbIterator, err: readErr}
	c.ldbIterator = it
	if !c.First() {
		t.Fatal("expected the first entry")
	}
	if c.Next() {
		t.Fatal("expected a read failure after the first entry")
	}
	if err := c.Close(); !errors.Is(err, readErr) {
		t.Errorf("Close returned %v, want the iterator read error", err)
	}
	if !it.released || !c.isClosed || c.ldbIterator != nil || c.bucket != nil {
		t.Error("Close did not release and clear the failed cursor")
	}
	if err := c.Close(); err == nil {
		t.Error("second Close succeeded")
	}
}
