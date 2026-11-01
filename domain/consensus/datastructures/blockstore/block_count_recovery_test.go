package blockstore

import (
	"errors"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/datastructures/testutils"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
)

// Ordinary block commits must keep the legacy schema until history deletion has
// actually introduced split counts. This also covers a fresh default-off node.
func TestLegacyBlockCountDoesNotCreateSplitMetadata(t *testing.T) {
	db, prefix, teardown := testutils.NewTestDB(t)
	defer teardown()
	instance, err := New(db, prefix, 10, 1<<20, false)
	if err != nil {
		t.Fatal(err)
	}
	store := instance.(*blockStore)
	shard := &blockStagingShard{store: store, toAdd: map[externalapi.DomainHash]*externalapi.DomainBlock{*testutils.Hash(1): nil}}
	tx, err := db.BeginUnindexed()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.RollbackUnlessClosed()
	if err := shard.commitCount(tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, key := range []model.DBKey{store.countedKey, store.deletedKey} {
		found, err := db.Has(key)
		if err != nil {
			t.Fatal(err)
		}
		if found {
			t.Fatalf("legacy commit created %q", key.Bytes())
		}
	}
}

type failingCountTransaction struct {
	model.DBTransaction
	failKey model.DBKey
	err     error
}

func (tx failingCountTransaction) Put(key model.DBKey, value []byte) error {
	if string(key.Bytes()) == string(tx.failKey.Bytes()) {
		return tx.err
	}
	return tx.DBTransaction.Put(key, value)
}

func TestFailedBlockCountWriteDoesNotAdvanceCachedCount(t *testing.T) {
	for _, split := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "split"}[split], func(t *testing.T) {
			db, prefix, teardown := testutils.NewTestDB(t)
			defer teardown()
			instance, err := New(db, prefix, 10, 1<<20, false)
			if err != nil {
				t.Fatal(err)
			}
			store := instance.(*blockStore)
			store.countCached = 10
			key := store.countKey
			if split {
				store.deletedCount = 2
				key = store.countedKey
			}
			shard := &blockStagingShard{store: store, toAdd: map[externalapi.DomainHash]*externalapi.DomainBlock{*testutils.Hash(1): nil}}
			tx, err := db.BeginUnindexed()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.RollbackUnlessClosed()
			injected := errors.New("count write failed")
			if err := shard.commitCount(failingCountTransaction{tx, key, injected}); !errors.Is(err, injected) {
				t.Fatalf("got %v", err)
			}
			if got := store.Count(model.NewStagingArea()); got != 10 {
				t.Fatalf("failed write changed count to %d", got)
			}
		})
	}
}
