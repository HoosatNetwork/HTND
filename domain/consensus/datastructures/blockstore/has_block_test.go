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

// TestHasBlockDoesNotReadTheBlock pins that HasBlock answers from a key-only Has: it used to Get, copy and
// deserialize the whole block, push it into the cache, and report any database fault as a missing block.
func TestHasBlockDoesNotReadTheBlock(t *testing.T) {
	dbManager, prefixBucket, teardown := testutils.NewTestDB(t)
	defer teardown()

	storeIface, err := New(dbManager, prefixBucket, 10, false)
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
	for _, test := range []struct {
		hash *externalapi.DomainHash
		want bool
	}{{stored, true}, {testutils.Hash(2), false}} {
		has, err := store.HasBlock(db, stagingArea, test.hash)
		if err != nil {
			t.Fatalf("HasBlock(%s): %v", test.hash, err)
		}
		if has != test.want {
			t.Errorf("HasBlock(%s) = %t, want %t", test.hash, has, test.want)
		}
	}
	if db.gets != 0 || db.hases != 2 {
		t.Errorf("HasBlock made %d Gets and %d Hases for two blocks, want 0 and 2", db.gets, db.hases)
	}
	if store.cache.Len() != 0 {
		t.Errorf("HasBlock added %d blocks to the cache", store.cache.Len())
	}

	faultyDB := &readCountingDB{DBReader: dbManager, err: errors.New("disk fault")}
	if _, err := store.HasBlock(faultyDB, stagingArea, stored); err == nil {
		t.Errorf("HasBlock reported a database fault as an answer instead of an error")
	}
}
