package consensusstatestore

import (
	"bytes"
	"math"
	"math/rand"
	"os"
	"strconv"
	"testing"

	consensusdatabase "github.com/HoosatNetwork/HTND/v2/domain/consensus/database"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/datastructures/testutils"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/utxo"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database/ldb"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database/pebble"
)

// newBackendTestDB opens a consensus DBManager on the named engine. The cursor engines differ in
// what Seek reports for a missing key, which is what the batched lookup has to get right on both.
func newBackendTestDB(tb testing.TB, engine string) (model.DBManager, database.Database) {
	tb.Helper()
	dir, err := os.MkdirTemp("", "htnd-utxos-by-outpoints-*")
	if err != nil {
		tb.Fatalf("MkdirTemp: %v", err)
	}
	var db database.Database
	switch engine {
	case "leveldb":
		db, err = ldb.NewLevelDB(dir, 8)
	case "pebble":
		db, err = pebble.NewPebbleDB(dir, 8)
	default:
		tb.Fatalf("unknown engine %s", engine)
	}
	if err != nil {
		_ = os.RemoveAll(dir)
		tb.Fatalf("opening %s: %v", engine, err)
	}
	tb.Cleanup(func() {
		_ = db.Close()
		_ = os.RemoveAll(dir)
	})
	return consensusdatabase.New(db), db
}

func randomOutpoint(random *rand.Rand) *externalapi.DomainOutpoint {
	var txID [externalapi.DomainHashSize]byte
	random.Read(txID[:])
	return externalapi.NewDomainOutpoint(externalapi.NewDomainTransactionIDFromByteArray(&txID), random.Uint32()%8)
}

func stageAndCommitUTXOs(tb testing.TB, dbManager model.DBManager, store model.ConsensusStateStore,
	toAdd, toRemove map[externalapi.DomainOutpoint]externalapi.UTXOEntry,
) {
	tb.Helper()
	diff, err := utxo.NewUTXODiffFromCollections(utxo.NewUTXOCollection(toAdd), utxo.NewUTXOCollection(toRemove))
	if err != nil {
		tb.Fatalf("NewUTXODiffFromCollections: %v", err)
	}
	stagingArea := model.NewStagingArea()
	store.StageVirtualUTXODiff(stagingArea, diff)
	dbTx, err := dbManager.Begin()
	if err != nil {
		tb.Fatalf("Begin: %v", err)
	}
	defer func() { _ = dbTx.RollbackUnlessClosed() }()
	if err := stagingArea.Commit(dbTx); err != nil {
		tb.Fatalf("stagingArea.Commit: %v", err)
	}
	if err := dbTx.Commit(); err != nil {
		tb.Fatalf("dbTx.Commit: %v", err)
	}
}

// TestUTXOsByOutpointsWithoutPopulatingCache pins that the batched, key-ordered lookup answers every
// outpoint exactly as the one-at-a-time lookup does - stored, missing (before, between and after the
// stored keys), requested twice, removed or added by a staged diff, or cached - and in request order,
// on both database engines, without adding to the cache.
func TestUTXOsByOutpointsWithoutPopulatingCache(t *testing.T) {
	for _, engine := range []string{"leveldb", "pebble"} {
		t.Run(engine, func(t *testing.T) {
			dbManager, _ := newBackendTestDB(t, engine)
			const cacheSize = 4
			prefixBucket := consensusdatabase.MakeBucket([]byte("utxos-by-outpoints-test"))
			store := New(prefixBucket, cacheSize, false).(*consensusStateStore)
			random := rand.New(rand.NewSource(1))

			stored := make([]*externalapi.DomainOutpoint, 40)
			toAdd := map[externalapi.DomainOutpoint]externalapi.UTXOEntry{}
			for i := range stored {
				stored[i] = randomOutpoint(random)
				toAdd[*stored[i]] = utxo.NewUTXOEntry(uint64(1000+i),
					&externalapi.ScriptPublicKey{Script: []byte{byte(i), 0xac}, Version: 0}, i%3 == 0, uint64(i))
			}
			stageAndCommitUTXOs(t, dbManager, store, toAdd, map[externalapi.DomainOutpoint]externalapi.UTXOEntry{})
			store.virtualUTXOSetCache.Clear()

			// A coin under the same suffix in the bucket sorting right after the UTXO set ("0" follows the
			// "/" ending the set's path) must not be found.
			neighbour := randomOutpoint(random)
			neighbourKey, err := store.utxoKey(neighbour)
			if err != nil {
				t.Fatalf("utxoKey: %v", err)
			}
			serializedEntry, err := serializeUTXOEntry(toAdd[*stored[0]])
			if err != nil {
				t.Fatalf("serializeUTXOEntry: %v", err)
			}
			otherBucketKey := prefixBucket.Bucket([]byte("virtual-utxo-set0")).Key(neighbourKey.Suffix())
			if err := dbManager.Put(otherBucketKey, serializedEntry); err != nil {
				t.Fatalf("Put: %v", err)
			}

			// Warm two coins into the cache, as block validation would.
			readStagingArea := model.NewStagingArea()
			for _, outpoint := range stored[:2] {
				if _, ok, err := store.UTXOByOutpoint(dbManager, readStagingArea, outpoint); err != nil || !ok {
					t.Fatalf("warming UTXOByOutpoint(%s): ok=%t err=%v", outpoint, ok, err)
				}
			}

			// Stage, without committing, the removal of one stored coin and the addition of a new one.
			stagedRemoved, stagedAdded := stored[5], randomOutpoint(random)
			diff, err := utxo.NewUTXODiffFromCollections(
				utxo.NewUTXOCollection(map[externalapi.DomainOutpoint]externalapi.UTXOEntry{
					*stagedAdded: testutils.UTXOEntry(77, 3),
				}),
				utxo.NewUTXOCollection(map[externalapi.DomainOutpoint]externalapi.UTXOEntry{
					*stagedRemoved: toAdd[*stagedRemoved],
				}))
			if err != nil {
				t.Fatalf("NewUTXODiffFromCollections: %v", err)
			}
			store.StageVirtualUTXODiff(readStagingArea, diff)

			var smallest, largest [externalapi.DomainHashSize]byte
			for i := range largest {
				largest[i] = 0xff
			}
			request := append([]*externalapi.DomainOutpoint{}, stored...)
			request = append(request,
				externalapi.NewDomainOutpoint(externalapi.NewDomainTransactionIDFromByteArray(&smallest), 0),
				externalapi.NewDomainOutpoint(externalapi.NewDomainTransactionIDFromByteArray(&largest), 9),
				randomOutpoint(random), randomOutpoint(random), neighbour, stagedAdded, stored[3], stored[0])
			random.Shuffle(len(request), func(i, j int) { request[i], request[j] = request[j], request[i] })

			entries := make([]externalapi.UTXOEntry, len(request))
			err = store.UTXOsByOutpointsWithoutPopulatingCache(dbManager, readStagingArea, request, entries)
			if err != nil {
				t.Fatalf("UTXOsByOutpointsWithoutPopulatingCache: %v", err)
			}
			found := 0
			for i, outpoint := range request {
				if outpoint.Equal(stagedRemoved) {
					// The single lookup answers a staged removal with a plain error rather than a not-found.
					if entries[i] != nil {
						t.Errorf("%s: got %v for a coin the staged diff removes", outpoint, entries[i])
					}
					continue
				}
				want, ok, err := store.UTXOByOutpointWithoutPopulatingCache(dbManager, readStagingArea, outpoint)
				if err != nil && !consensusdatabase.IsNotFoundError(err) {
					t.Fatalf("UTXOByOutpointWithoutPopulatingCache(%s): %v", outpoint, err)
				}
				if !ok {
					if entries[i] != nil {
						t.Errorf("%s: got %v, want no entry", outpoint, entries[i])
					}
					continue
				}
				found++
				if entries[i] == nil || !entries[i].Equal(want) {
					t.Errorf("%s: got %v, want %v", outpoint, entries[i], want)
				}
			}
			// 40 stored, less the staged removal, plus the staged addition and the two repeats.
			if want := len(stored) - 1 + 1 + 2; found != want {
				t.Errorf("found %d entries, want %d", found, want)
			}
			if got := store.CacheLen(); got != 2 {
				t.Errorf("the batched lookup changed the cache from 2 entries to %d", got)
			}
		})
	}
}

// BenchmarkVirtualUTXOLookup compares reading a wallet's worth of coins scattered over a large UTXO
// set one Get at a time with reading them through one key-ordered cursor. Sizes are set with
// HTND_BENCH_UTXO_SET and HTND_BENCH_LOOKUPS.
func BenchmarkVirtualUTXOLookup(b *testing.B) {
	setSize := benchEnvInt("HTND_BENCH_UTXO_SET", 1_000_000)
	lookupCount := benchEnvInt("HTND_BENCH_LOOKUPS", 20_000)

	dbManager, db := newBackendTestDB(b, "pebble")
	store := New(consensusdatabase.MakeBucket([]byte("bench")), 1, false).(*consensusStateStore)
	random := rand.New(rand.NewSource(2))

	all := make([]*externalapi.DomainOutpoint, 0, setSize)
	stored := make([]externalapi.UTXOEntry, 0, setSize)
	const batchSize = 50_000
	for len(all) < setSize {
		toAdd := map[externalapi.DomainOutpoint]externalapi.UTXOEntry{}
		for i := 0; i < batchSize && len(all) < setSize; i++ {
			outpoint := randomOutpoint(random)
			entry := utxo.NewUTXOEntry(uint64(len(all)),
				&externalapi.ScriptPublicKey{Script: make([]byte, 34), Version: 0}, false, uint64(len(all)))
			all = append(all, outpoint)
			stored = append(stored, entry)
			toAdd[*outpoint] = entry
		}
		stageAndCommitUTXOs(b, dbManager, store, toAdd, map[externalapi.DomainOutpoint]externalapi.UTXOEntry{})
	}
	if err := db.(*pebble.DB).Compact(); err != nil {
		b.Fatalf("Compact: %v", err)
	}
	store.virtualUTXOSetCache.Clear()

	lookups := make([]*externalapi.DomainOutpoint, lookupCount)
	preferred := make([]externalapi.UTXOEntry, lookupCount)
	for i := range lookups {
		at := random.Intn(len(all))
		lookups[i] = all[at]
		preferred[i] = stored[at]
	}
	entries := make([]externalapi.UTXOEntry, lookupCount)

	b.Run("get-per-outpoint", func(b *testing.B) {
		for b.Loop() {
			stagingArea := model.NewStagingArea()
			for i, outpoint := range lookups {
				entry, _, err := store.UTXOByOutpointWithoutPopulatingCache(dbManager, stagingArea, outpoint)
				if err != nil {
					b.Fatalf("lookup: %v", err)
				}
				entries[i] = entry
			}
		}
	})
	b.Run("sorted-cursor", func(b *testing.B) {
		for b.Loop() {
			err := store.UTXOsByOutpointsWithoutPopulatingCache(dbManager, model.NewStagingArea(), lookups, entries)
			if err != nil {
				b.Fatalf("lookup: %v", err)
			}
		}
	})
	b.Run("sorted-cursor-reuse", func(b *testing.B) {
		for b.Loop() {
			ordered, err := store.PrepareOrderedVirtualUTXOKeys(lookups)
			if err != nil {
				b.Fatalf("PrepareOrderedVirtualUTXOKeys: %v", err)
			}
			err = store.UTXOsByOrderedKeysWithoutPopulatingCache(dbManager, model.NewStagingArea(), lookups, ordered, entries, preferred)
			if err != nil {
				b.Fatalf("lookup: %v", err)
			}
		}
	})
}

// TestOrderedVirtualUTXOKeyMatchesUTXOKey pins that the bulk lookup's reused encoder writes the
// same bytes as the single-key path. A mismatch would make every seek miss.
func TestOrderedVirtualUTXOKeyMatchesUTXOKey(t *testing.T) {
	store := New(consensusdatabase.MakeBucket([]byte("utxo-key-encoding")), 1, false).(*consensusStateStore)
	random := rand.New(rand.NewSource(7))
	zeroID := randomOutpoint(random)
	maxID := randomOutpoint(random)
	outpoints := []*externalapi.DomainOutpoint{
		randomOutpoint(random),
		externalapi.NewDomainOutpoint(&zeroID.TransactionID, 0),
		externalapi.NewDomainOutpoint(&maxID.TransactionID, math.MaxUint32),
	}
	ordered, err := store.PrepareOrderedVirtualUTXOKeys(outpoints)
	if err != nil {
		t.Fatalf("PrepareOrderedVirtualUTXOKeys: %v", err)
	}
	if len(ordered) != len(outpoints) {
		t.Fatalf("got %d keys for %d outpoints", len(ordered), len(outpoints))
	}
	for _, item := range ordered {
		want, err := store.utxoKey(outpoints[item.Index])
		if err != nil {
			t.Fatalf("utxoKey: %v", err)
		}
		if !bytes.Equal(item.Key, want.Bytes()) {
			t.Fatalf("outpoint %s: encoded key %x, utxoKey %x", outpoints[item.Index], item.Key, want.Bytes())
		}
	}
	for i := 1; i < len(ordered); i++ {
		if bytes.Compare(ordered[i-1].Key, ordered[i].Key) > 0 {
			t.Fatalf("keys are not sorted: %x then %x", ordered[i-1].Key, ordered[i].Key)
		}
	}
}

// TestUTXOsByOrderedKeysReusesAMatchingEntry pins two things the RPC filter relies on. An entry
// whose bytes are what the database stored is returned as that same value, and an entry that only
// differs by BlockDAAScore is not. It looks up the middle of three stored coins on its own, so the
// cursor bound has a key on either side of the range it is allowed to see.
func TestUTXOsByOrderedKeysReusesAMatchingEntry(t *testing.T) {
	for _, engine := range []string{"leveldb", "pebble"} {
		t.Run(engine, func(t *testing.T) {
			dbManager, _ := newBackendTestDB(t, engine)
			store := New(consensusdatabase.MakeBucket([]byte("utxo-entry-reuse")), 4, false).(*consensusStateStore)
			random := rand.New(rand.NewSource(9))

			outpoints := make([]*externalapi.DomainOutpoint, 3)
			stored := make([]externalapi.UTXOEntry, 3)
			toAdd := map[externalapi.DomainOutpoint]externalapi.UTXOEntry{}
			for i := range outpoints {
				outpoints[i] = randomOutpoint(random)
				stored[i] = utxo.NewUTXOEntry(uint64(1000+i),
					&externalapi.ScriptPublicKey{Script: []byte{byte(i), 0xac}, Version: 0}, i == 1, uint64(50+i))
				toAdd[*outpoints[i]] = stored[i]
			}
			stageAndCommitUTXOs(t, dbManager, store, toAdd, map[externalapi.DomainOutpoint]externalapi.UTXOEntry{})
			store.virtualUTXOSetCache.Clear()

			ordered, err := store.PrepareOrderedVirtualUTXOKeys(outpoints)
			if err != nil {
				t.Fatalf("PrepareOrderedVirtualUTXOKeys: %v", err)
			}
			middle := -1
			for i, item := range ordered {
				if i > 0 && i < len(ordered)-1 {
					middle = item.Index
				}
			}
			if middle < 0 {
				t.Fatal("three distinct keys should have a middle one")
			}

			entries := make([]externalapi.UTXOEntry, len(outpoints))
			preferred := make([]externalapi.UTXOEntry, len(outpoints))
			preferred[middle] = stored[middle]
			err = store.UTXOsByOrderedKeysWithoutPopulatingCache(dbManager, model.NewStagingArea(),
				outpoints, ordered[1:2], entries, preferred)
			if err != nil {
				t.Fatalf("middle lookup: %v", err)
			}
			if entries[middle] != stored[middle] {
				t.Fatalf("the middle coin's bytes match the entry already held; got a new %#v", entries[middle])
			}
			for i, entry := range entries {
				if i != middle && entry != nil {
					t.Fatalf("the bounded lookup returned outpoint %d, which is outside its key range", i)
				}
			}

			// The index does not hold the object consensus stored. It holds one with the same fields.
			copied := utxo.NewUTXOEntry(stored[middle].Amount(), stored[middle].ScriptPublicKey(),
				stored[middle].IsCoinbase(), stored[middle].BlockDAAScore())
			preferred[middle] = copied
			entries = make([]externalapi.UTXOEntry, len(outpoints))
			err = store.UTXOsByOrderedKeysWithoutPopulatingCache(dbManager, model.NewStagingArea(),
				outpoints, ordered[1:2], entries, preferred)
			if err != nil {
				t.Fatalf("copied-entry lookup: %v", err)
			}
			if entries[middle] != copied {
				t.Fatalf("an entry with the same fields as the stored coin must be reused, got %#v", entries[middle])
			}

			drifted := utxo.NewUTXOEntry(stored[middle].Amount(), stored[middle].ScriptPublicKey(),
				stored[middle].IsCoinbase(), stored[middle].BlockDAAScore()+1)
			for i := range preferred {
				preferred[i] = stored[i]
			}
			preferred[middle] = drifted
			entries = make([]externalapi.UTXOEntry, len(outpoints))
			err = store.UTXOsByOrderedKeysWithoutPopulatingCache(dbManager, model.NewStagingArea(),
				outpoints, ordered, entries, preferred)
			if err != nil {
				t.Fatalf("full lookup: %v", err)
			}
			if entries[middle] == drifted || entries[middle] == nil || !entries[middle].Equal(stored[middle]) {
				t.Fatalf("a drifted stamp must be replaced by virtual's entry, got %#v", entries[middle])
			}
			for i, entry := range entries {
				if i == middle {
					continue
				}
				if entry != stored[i] {
					t.Fatalf("outpoint %d matched the entry already held and was copied instead", i)
				}
			}
		})
	}
}

func benchEnvInt(name string, defaultValue int) int {
	value := os.Getenv(name)
	if value == "" {
		return defaultValue
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		panic(err)
	}
	return parsed
}
