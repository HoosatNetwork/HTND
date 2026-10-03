package blockstore

import (
	"errors"
	"math/big"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/datastructures/testutils"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/blockheader"
)

// readCountingDB counts the reads a store makes, and fails them all when err is set.
type readCountingDB struct {
	model.DBReader
	gets, hases int
	err         error
}

func (db *readCountingDB) Get(key model.DBKey) ([]byte, error) {
	db.gets++
	if db.err != nil {
		return nil, db.err
	}
	return db.DBReader.Get(key)
}

func (db *readCountingDB) Has(key model.DBKey) (bool, error) {
	db.hases++
	if db.err != nil {
		return false, db.err
	}
	return db.DBReader.Has(key)
}

// TestHasBlockRemembersAFoundBlockAndReportsFaults pins that HasBlock answers from the key alone and
// remembers a block it finds, so the same block announced by every peer asks the database once,
// that a deleted block is forgotten, and that a database fault is an error rather than "block not
// present", which is what it used to be reported as.
func TestHasBlockRemembersAFoundBlockAndReportsFaults(t *testing.T) {
	dbManager, prefixBucket, teardown := testutils.NewTestDB(t)
	defer teardown()

	storeIface, err := New(dbManager, prefixBucket, 10, 1<<20, false)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	store := storeIface.(*blockStore)

	header := blockheader.NewImmutableBlockHeader(1, []externalapi.BlockLevelParents{}, testutils.Hash(10),
		testutils.Hash(11), testutils.Hash(12), 123, 0x1d00ffff, 7, 100, 200, big.NewInt(0), testutils.Hash(13))
	stored := testutils.Hash(1)
	stagingArea := model.NewStagingArea()
	store.Stage(stagingArea, stored, &externalapi.DomainBlock{Header: header, Transactions: []*externalapi.DomainTransaction{}})
	testutils.Commit(t, dbManager, stagingArea)
	store.cache.Clear()

	db := &readCountingDB{DBReader: dbManager}
	stagingArea = model.NewStagingArea()
	// The stored block twice - as two peers announcing it would - and a missing one.
	for _, test := range []struct {
		hash *externalapi.DomainHash
		want bool
	}{{stored, true}, {stored, true}, {testutils.Hash(2), false}} {
		has, err := store.HasBlock(db, stagingArea, test.hash)
		if err != nil {
			t.Fatalf("HasBlock(%s): %v", test.hash, err)
		}
		if has != test.want {
			t.Errorf("HasBlock(%s) = %t, want %t", test.hash, has, test.want)
		}
	}
	if db.gets != 0 {
		t.Errorf("HasBlock read %d block values, want 0: whether a block exists is a question about its key", db.gets)
	}
	if db.hases != 2 {
		t.Errorf("HasBlock asked the database %d times for a block asked about twice and a missing one, "+
			"want 2: the second ask must be a cache hit", db.hases)
	}
	if !store.existsCache.Has(stored) {
		t.Errorf("HasBlock did not remember the block it found")
	}
	if store.cache.Has(stored) {
		t.Errorf("HasBlock put the block body in the block cache, which no caller of HasBlock uses")
	}

	// A deleted block must stop being reported, even though HasBlock remembered it.
	stagingArea = model.NewStagingArea()
	store.Delete(stagingArea, stored)
	testutils.Commit(t, dbManager, stagingArea)
	if has, err := store.HasBlock(db, model.NewStagingArea(), stored); err != nil || has {
		t.Errorf("HasBlock after Delete = %t, %v; want false, nil", has, err)
	}

	faultyDB := &readCountingDB{DBReader: dbManager, err: errors.New("disk fault")}
	if _, err := store.HasBlock(faultyDB, stagingArea, testutils.Hash(3)); err == nil {
		t.Errorf("HasBlock reported a database fault as an answer instead of an error")
	}
}
