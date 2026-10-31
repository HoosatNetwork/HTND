package pebble

import (
	"bytes"
	"errors"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database"
)

func TestPP42SnapshotPinsReadsAndReopenedCursors(t *testing.T) {
	t.Setenv("HTND_TEST_MODE", "0")
	t.Setenv("HTND_PEBBLE_DISABLE_WAL", "0")
	db, e := OpenPebbleDB(t.TempDir(), 8)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	bucket := database.MakeBucket([]byte("snapshot"))
	k := bucket.Key([]byte("a"))
	if e = db.Put(k, []byte("old")); e != nil {
		t.Fatal(e)
	}
	s, e := db.NewSnapshot()
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	for i := 0; i < 3; i++ {
		if e = db.Put(k, []byte("new")); e != nil {
			t.Fatal(e)
		}
		v, e := s.Get(k)
		if e != nil || !bytes.Equal(v, []byte("old")) {
			t.Fatal(string(v), e)
		}
		c, e := s.Cursor(bucket)
		if e != nil {
			t.Fatal(e)
		}
		if !c.First() {
			t.Fatal("empty snapshot")
		}
		v, e = c.Value()
		if e != nil || !bytes.Equal(v, []byte("old")) {
			t.Fatal(string(v), e)
		}
		if e = c.Close(); e != nil {
			t.Fatal(e)
		}
		if e = db.Delete(k); e != nil {
			t.Fatal(e)
		}
	}
}

// WAL is irrelevant to process-local visibility, and is not a recovery promise.
func TestPP05SnapshotWithoutWALAndLifetime(t *testing.T) {
	t.Setenv("HTND_TEST_MODE", "1")
	db, err := OpenPebbleDB(t.TempDir(), 8)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	bucket := database.MakeBucket([]byte("lifetime"))
	key := bucket.Key([]byte("key"))
	if err = db.Put(key, []byte("value")); err != nil {
		t.Fatal(err)
	}
	s, err := db.NewSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	v, err := s.Get(key)
	if err != nil {
		t.Fatal(err)
	}
	v[0] = 'X'
	v, err = s.Get(key)
	if err != nil || string(v) != "value" {
		t.Fatalf("Get bytes alias: %q %v", v, err)
	}
	if found, err := s.Has(key); err != nil || !found {
		t.Fatalf("Has: %t %v", found, err)
	}
	if found, err := s.Has(bucket.Key([]byte("missing"))); err != nil || found {
		t.Fatalf("Has missing: %t %v", found, err)
	}
	if _, err = s.Get(bucket.Key([]byte("missing"))); !errors.Is(err, database.ErrNotFound) {
		t.Fatal(err)
	}
	c, err := s.Cursor(bucket)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); !errors.Is(err, database.ErrSnapshotCursorsOpen) {
		t.Fatalf("open cursor close: %v", err)
	}
	if !c.First() {
		t.Fatal("refused close invalidated cursor")
	}
	if err = c.Close(); err != nil {
		t.Fatal(err)
	}
	if err = c.Close(); err == nil {
		t.Fatal("duplicate cursor close succeeded")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Get(key); !errors.Is(err, database.ErrSnapshotClosed) {
		t.Fatal(err)
	}
	if _, err = s.Has(key); !errors.Is(err, database.ErrSnapshotClosed) {
		t.Fatal(err)
	}
	if _, err = s.Cursor(bucket); !errors.Is(err, database.ErrSnapshotClosed) {
		t.Fatal(err)
	}
	if err = s.Close(); !errors.Is(err, database.ErrSnapshotClosed) {
		t.Fatal(err)
	}
}

func TestPP05SnapshotConcurrentLiveMutation(t *testing.T) {
	t.Setenv("HTND_TEST_MODE", "1")
	db, err := OpenPebbleDB(t.TempDir(), 8)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	bucket := database.MakeBucket([]byte("concurrent"))
	key := bucket.Key([]byte("base"))
	later := bucket.Key([]byte("later"))
	if err = db.Put(key, []byte("pinned")); err != nil {
		t.Fatal(err)
	}
	s, err := db.NewSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	done := make(chan error, 1)
	go func() {
		for i := 0; i < 50; i++ {
			if err := db.Put(key, []byte("changed")); err != nil {
				done <- err
				return
			}
			if err := db.Put(later, []byte("new")); err != nil {
				done <- err
				return
			}
			if err := db.Delete(key); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	// Join even on a fatal assertion, before deferred snapshot/database closure.
	defer func() {
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	for i := 0; i < 50; i++ {
		v, err := s.Get(key)
		if err != nil || string(v) != "pinned" {
			t.Fatalf("Get: %q %v", v, err)
		}
		found, err := s.Has(later)
		if err != nil || found {
			t.Fatalf("later key leaked: %t %v", found, err)
		}
		c, err := s.Cursor(bucket)
		if err != nil {
			t.Fatal(err)
		}
		if !c.First() {
			c.Close()
			t.Fatal("missing base")
		}
		v, err = c.Value()
		if err != nil || string(v) != "pinned" {
			c.Close()
			t.Fatalf("cursor: %q %v", v, err)
		}
		if c.Next() {
			c.Close()
			t.Fatal("later key leaked into cursor")
		}
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
