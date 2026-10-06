package consensusstatestore

import (
	"errors"
	"math/rand"
	"testing"

	consensusdatabase "github.com/HoosatNetwork/HTND/v2/domain/consensus/database"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/datastructures/testutils"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/utxo"
)

// readCountingDB counts the reads a store makes, and fails them all when err is set.
type readCountingDB struct {
	model.DBReader
	reads int
	err   error
}

func (db *readCountingDB) Get(key model.DBKey) ([]byte, error) {
	db.reads++
	if db.err != nil {
		return nil, db.err
	}
	return db.DBReader.Get(key)
}

func (db *readCountingDB) Has(key model.DBKey) (bool, error) {
	db.reads++
	if db.err != nil {
		return false, db.err
	}
	return db.DBReader.Has(key)
}

// TestLookupUTXOByOutpoint pins that LookupUTXOByOutpoint answers what HasUTXOByOutpoint followed by
// UTXOByOutpoint answered - for a stored, cached, missing, staged-removed and staged-added coin - with no
// database read for a cached or staged coin and one for any other, where Has-then-Get made one read for a
// cached coin and two for a stored one. A database fault is an error, not a missing coin.
func TestLookupUTXOByOutpoint(t *testing.T) {
	dbManager, prefixBucket, teardown := testutils.NewTestDB(t)
	defer teardown()
	store := New(prefixBucket, 10, false).(*consensusStateStore)

	random := rand.New(rand.NewSource(3))
	cached, stored, removed := randomOutpoint(random), randomOutpoint(random), randomOutpoint(random)
	added, missing := randomOutpoint(random), randomOutpoint(random)
	toAdd := map[externalapi.DomainOutpoint]externalapi.UTXOEntry{
		*cached:  testutils.UTXOEntry(1, 1),
		*stored:  testutils.UTXOEntry(2, 2),
		*removed: testutils.UTXOEntry(3, 3),
	}
	stageAndCommitUTXOs(t, dbManager, store, toAdd, map[externalapi.DomainOutpoint]externalapi.UTXOEntry{})
	store.virtualUTXOSetCache.Clear()

	stagingArea := model.NewStagingArea()
	if _, ok, err := store.UTXOByOutpoint(dbManager, stagingArea, cached); err != nil || !ok {
		t.Fatalf("warming the cache: ok=%t err=%v", ok, err)
	}
	diff, err := utxo.NewUTXODiffFromCollections(
		utxo.NewUTXOCollection(map[externalapi.DomainOutpoint]externalapi.UTXOEntry{*added: testutils.UTXOEntry(4, 4)}),
		utxo.NewUTXOCollection(map[externalapi.DomainOutpoint]externalapi.UTXOEntry{*removed: toAdd[*removed]}))
	if err != nil {
		t.Fatalf("NewUTXODiffFromCollections: %v", err)
	}
	store.StageVirtualUTXODiff(stagingArea, diff)

	for _, test := range []struct {
		name      string
		outpoint  *externalapi.DomainOutpoint
		wantFound bool
		wantReads int
	}{
		{"cached", cached, true, 0},
		{"staged-added", added, true, 0},
		{"staged-removed", removed, false, 0},
		{"stored", stored, true, 1},
		{"missing", missing, false, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			hasDB := &readCountingDB{DBReader: dbManager}
			has, err := store.HasUTXOByOutpoint(hasDB, stagingArea, test.outpoint)
			if err != nil {
				t.Fatalf("HasUTXOByOutpoint: %v", err)
			}
			var want externalapi.UTXOEntry
			if has {
				want, _, err = store.UTXOByOutpointWithoutPopulatingCache(hasDB, stagingArea, test.outpoint)
				if err != nil {
					t.Fatalf("UTXOByOutpointWithoutPopulatingCache: %v", err)
				}
			}

			for _, lookup := range []struct {
				name     string
				populate bool
				fn       func(model.DBReader, *model.StagingArea, *externalapi.DomainOutpoint) (externalapi.UTXOEntry, bool, error)
			}{
				{"without-populating", false, store.LookupUTXOByOutpointWithoutPopulatingCache},
				{"populating", true, store.LookupUTXOByOutpoint},
			} {
				cacheLen := store.CacheLen()
				db := &readCountingDB{DBReader: dbManager}
				entry, found, err := lookup.fn(db, stagingArea, test.outpoint)
				if err != nil {
					t.Fatalf("%s: %v", lookup.name, err)
				}
				if found != has || found != test.wantFound {
					t.Errorf("%s: found=%t, Has says %t, want %t", lookup.name, found, has, test.wantFound)
				}
				if (entry == nil) != (want == nil) || (entry != nil && !entry.Equal(want)) {
					t.Errorf("%s: entry %v, want %v", lookup.name, entry, want)
				}
				if db.reads != test.wantReads {
					t.Errorf("%s: %d database reads, want %d", lookup.name, db.reads, test.wantReads)
				}
				grew := store.CacheLen() > cacheLen
				if grew && !lookup.populate {
					t.Errorf("%s: added a miss to the cache", lookup.name)
				}
				if lookup.populate && test.name == "stored" && !store.virtualUTXOSetCache.Has(test.outpoint) {
					t.Errorf("%s: did not add the stored coin to the cache", lookup.name)
				}
			}
		})
	}

	faultyDB := &readCountingDB{DBReader: dbManager, err: errors.New("disk fault")}
	if _, _, err := store.LookupUTXOByOutpoint(faultyDB, stagingArea, missing); err == nil ||
		consensusdatabase.IsNotFoundError(err) {
		t.Errorf("a database fault came back as %v, want the fault itself", err)
	}
}

// BenchmarkPopulateInputLookup compares Has-then-Get, which block validation did for every transaction
// input, with one LookupUTXOByOutpoint, for coins in the cache and coins only in the database.
func BenchmarkPopulateInputLookup(b *testing.B) {
	const coinCount = 200_000
	dbManager, _ := newBackendTestDB(b, "pebble")
	store := New(consensusdatabase.MakeBucket([]byte("bench")), coinCount, false).(*consensusStateStore)
	random := rand.New(rand.NewSource(4))

	outpoints := make([]*externalapi.DomainOutpoint, coinCount)
	toAdd := map[externalapi.DomainOutpoint]externalapi.UTXOEntry{}
	for i := range outpoints {
		outpoints[i] = randomOutpoint(random)
		toAdd[*outpoints[i]] = utxo.NewUTXOEntry(uint64(i),
			&externalapi.ScriptPublicKey{Script: make([]byte, 34), Version: 0}, false, uint64(i))
	}
	stageAndCommitUTXOs(b, dbManager, store, toAdd, map[externalapi.DomainOutpoint]externalapi.UTXOEntry{})

	hasThenGet := func(stagingArea *model.StagingArea, outpoint *externalapi.DomainOutpoint) {
		has, err := store.HasUTXOByOutpoint(dbManager, stagingArea, outpoint)
		if err != nil || !has {
			b.Fatalf("Has: %t %v", has, err)
		}
		if _, _, err := store.UTXOByOutpointWithoutPopulatingCache(dbManager, stagingArea, outpoint); err != nil {
			b.Fatalf("Get: %v", err)
		}
	}
	lookup := func(stagingArea *model.StagingArea, outpoint *externalapi.DomainOutpoint) {
		if _, found, err := store.LookupUTXOByOutpointWithoutPopulatingCache(dbManager, stagingArea, outpoint); err != nil || !found {
			b.Fatalf("Lookup: %t %v", found, err)
		}
	}
	for _, cacheState := range []string{"cached", "uncached"} {
		for _, method := range []struct {
			name string
			fn   func(*model.StagingArea, *externalapi.DomainOutpoint)
		}{{"has-then-get", hasThenGet}, {"lookup", lookup}} {
			b.Run(cacheState+"/"+method.name, func(b *testing.B) {
				if cacheState == "uncached" {
					store.virtualUTXOSetCache.Clear()
				}
				stagingArea := model.NewStagingArea()
				i := 0
				for b.Loop() {
					method.fn(stagingArea, outpoints[i%coinCount])
					i++
				}
			})
		}
	}
}
