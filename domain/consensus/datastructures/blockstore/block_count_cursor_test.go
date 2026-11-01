package blockstore

import (
	"errors"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/datastructures/testutils"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
)

type failedRecountReader struct {
	model.DBReader
	err error
}

func (r failedRecountReader) Cursor(bucket model.DBBucket) (model.DBCursor, error) {
	cursor, err := r.DBReader.Cursor(bucket)
	if err != nil {
		return nil, err
	}
	return &failedRecountCursor{DBCursor: cursor, err: r.err}, nil
}

type failedRecountCursor struct {
	model.DBCursor
	err error
}

func (c *failedRecountCursor) First() bool { return false }
func (c *failedRecountCursor) Close() error {
	return errors.Join(c.DBCursor.Close(), c.err)
}

func TestBlockCountRecoveryRejectsFailedIteration(t *testing.T) {
	db, prefix, closeDB := testutils.NewTestDB(t)
	defer closeDB()
	instance, err := New(db, prefix, 2, 1<<20, false)
	if err != nil {
		t.Fatal(err)
	}
	store := instance.(*blockStore)
	// Contradictory counters force the recovery recount. An iterator error must
	// not turn the unread bodies into a successful zero count for normalization.
	count, err := store.serializeBlockCount(1)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Put(store.countedKey, count); err != nil {
		t.Fatal(err)
	}
	if err := db.Put(store.hashAsKey(testutils.Hash(1)), []byte("body")); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("body iterator failed")
	if _, err := New(failedRecountReader{db, injected}, prefix, 2, 1<<20, false); !errors.Is(err, injected) {
		t.Fatalf("partial recount accepted: %v", err)
	}
	recovered, err := New(db, prefix, 2, 1<<20, false)
	if err != nil {
		t.Fatal(err)
	}
	if count := recovered.Count(model.NewStagingArea()); count != 1 {
		t.Fatalf("recovered count %d, want 1", count)
	}
}
