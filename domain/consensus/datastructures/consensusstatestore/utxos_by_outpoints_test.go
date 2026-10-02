package consensusstatestore

import (
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
	const batchSize = 50_000
	for len(all) < setSize {
		toAdd := map[externalapi.DomainOutpoint]externalapi.UTXOEntry{}
		for i := 0; i < batchSize && len(all) < setSize; i++ {
			outpoint := randomOutpoint(random)
			all = append(all, outpoint)
			toAdd[*outpoint] = utxo.NewUTXOEntry(uint64(len(all)),
				&externalapi.ScriptPublicKey{Script: make([]byte, 34), Version: 0}, false, uint64(len(all)))
		}
		stageAndCommitUTXOs(b, dbManager, store, toAdd, map[externalapi.DomainOutpoint]externalapi.UTXOEntry{})
	}
	if err := db.(*pebble.DB).Compact(); err != nil {
		b.Fatalf("Compact: %v", err)
	}
	store.virtualUTXOSetCache.Clear()

	lookups := make([]*externalapi.DomainOutpoint, lookupCount)
	for i := range lookups {
		lookups[i] = all[random.Intn(len(all))]
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
