package database

import (
	"errors"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	rawdb "github.com/HoosatNetwork/HTND/v2/infrastructure/db/database"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database/ldb"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database/pebble"
)

type failingSnapshotBackend struct {
	rawdb.Database
	err error
}

func (b *failingSnapshotBackend) NewSnapshot() (rawdb.Snapshot, error) { return nil, b.err }

func TestPP05SnapshotCapabilityFailure(t *testing.T) {
	raw, err := ldb.NewLevelDB(t.TempDir(), 8)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	db := New(raw).(*dbManager)
	if view, err := db.NewSnapshot(); view != nil || !errors.Is(err, ErrSnapshotUnsupported) {
		t.Fatalf("unsupported: %v %v", view, err)
	}
	// Declining the optional capability leaves the supported ordinary path usable.
	key := MakeBucket([]byte("baseline")).Key([]byte("key"))
	if err := db.Put(key, []byte("value")); err != nil {
		t.Fatal(err)
	}
	if value, err := db.Get(key); err != nil || string(value) != "value" {
		t.Fatalf("baseline: %q %v", value, err)
	}
	want := errors.New("snapshot creation failed")
	failing := New(&failingSnapshotBackend{err: want}).(*dbManager)
	if view, err := failing.NewSnapshot(); view != nil || !errors.Is(err, want) {
		t.Fatalf("failure: %v %v", view, err)
	}
}

func TestPP05ReadSnapshotRejectsEveryWrite(t *testing.T) {
	raw, err := pebble.OpenPebbleDB(t.TempDir(), 8)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	db := New(raw).(*dbManager)
	key := MakeBucket([]byte("write-refusal")).Key([]byte("key"))
	if err = db.Put(key, []byte("original")); err != nil {
		t.Fatal(err)
	}
	view, err := db.NewSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	for name, write := range map[string]func() error{
		"put":    func() error { return view.Put(key, []byte("changed")) },
		"delete": func() error { return view.Delete(key) },
	} {
		if err := write(); !errors.Is(err, ErrReadOnlySnapshot) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	for _, begin := range []func() (model.DBTransaction, error){view.Begin, view.BeginUnindexed} {
		if tx, err := begin(); tx != nil || !errors.Is(err, ErrReadOnlySnapshot) {
			t.Fatalf("begin: %v %v", tx, err)
		}
	}
	if value, err := db.Get(key); err != nil || string(value) != "original" {
		t.Fatalf("write escaped: %q %v", value, err)
	}
}
