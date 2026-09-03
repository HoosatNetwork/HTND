package utxoderive

import (
	consensusdatabase "github.com/HoosatNetwork/HTND/domain/consensus/database"
	"github.com/HoosatNetwork/HTND/domain/consensus/datastructures/blockheaderstore"
	"github.com/HoosatNetwork/HTND/domain/consensus/datastructures/blockstore"
	"github.com/HoosatNetwork/HTND/domain/consensus/datastructures/daablocksstore"
	"github.com/HoosatNetwork/HTND/domain/consensus/datastructures/ghostdagdatastore"
	"github.com/HoosatNetwork/HTND/domain/consensus/datastructures/pruningstore"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	infrastructuredatabase "github.com/HoosatNetwork/HTND/infrastructure/db/database"
	"github.com/HoosatNetwork/HTND/infrastructure/db/database/ldb"
	"github.com/pkg/errors"
)

// derivedStoreBuckets are the stores a replay must never read or inherit, keyed by the bucket
// or key name they occupy under the consensus prefix.
//
// Every one of them is downstream of the fault: the served pruning-point bucket IS the export
// under suspicion, the diff store is the algebra being bypassed, the multiset store is the
// incremental chain rooted at a rewritten import anchor, and the index is a projection of the
// virtual set. Copying any of them into a destination datadir silently reintroduces the very
// lineage this replay exists to escape - which is why WipeDerivedStores deletes them and
// VerifyDerivedStoresAbsent then refuses to proceed if any survived.
var derivedStoreBuckets = [][]byte{
	[]byte("virtual-utxo-set"),
	[]byte("importing-pruning-point-utxo-set"),
	[]byte("pruning-point-utxo-set"),
	[]byte("imported-pruning-point-utxos"),
	[]byte("imported-pruning-point-multiset"),
	[]byte("updating-pruning-point-utxo-set"),
	[]byte("pruning-utxo-verified"),
	[]byte("utxo-diffs"),
	[]byte("utxo-diff-children"),
	[]byte("multiset"),
	[]byte("multisets"),
	[]byte("utxo-index"),
	[]byte("utxo-index-counts"),
	[]byte("utxo-index-virtual-parents"),
	[]byte("utxo-index-circulating-supply"),
	[]byte("utxo-index-counts-initialized"),
}

// OpenLevelDB opens a datadir for a replay. cacheSizeMiB follows the node's own sizing.
func OpenLevelDB(path string, cacheSizeMiB int) (infrastructuredatabase.Database, error) {
	db, err := ldb.NewLevelDB(path, cacheSizeMiB)
	if err != nil {
		return nil, errors.Wrapf(err, "utxoderive: could not open %s. A datadir in use by a running node "+
			"cannot be opened a second time - stop the node, or copy the directory first", path)
	}
	return db, nil
}

// OpenStores constructs exactly the read-side stores a replay needs, over the given consensus
// prefix. It deliberately constructs no UTXO-bearing store: a Deriver that could read the
// virtual set, the served bucket, the diff chain or the multiset chain would be able to
// accidentally trust them.
func OpenStores(db infrastructuredatabase.Database, prefixBytes []byte, cacheSize int, preallocate bool) (Stores, error) {
	dbManager := consensusdatabase.New(db)
	prefixBucket := consensusdatabase.MakeBucket(prefixBytes)

	blockStore, err := blockstore.New(dbManager, prefixBucket, cacheSize, preallocate)
	if err != nil {
		return Stores{}, err
	}
	blockHeaderStore, err := blockheaderstore.New(dbManager, prefixBucket, cacheSize, preallocate)
	if err != nil {
		return Stores{}, err
	}

	// Level 0 is the block-level GHOSTDAG data that merge sets and selected parents come from.
	ghostdagDataStore := ghostdagdatastore.New(prefixBucket.Bucket([]byte{0}), cacheSize, preallocate)

	return Stores{
		DatabaseContext:   dbManager,
		BlockStore:        blockStore,
		BlockHeaderStore:  blockHeaderStore,
		GHOSTDAGDataStore: ghostdagDataStore,
		DAABlocksStore:    daablocksstore.New(prefixBucket, cacheSize, cacheSize, preallocate),
		PruningStore:      pruningstore.New(prefixBucket, 2, preallocate),
	}, nil
}

// WipeDerivedStores deletes every derived store from a destination datadir.
//
// This is the "copy by store" step. A key-value datadir cannot be copied selectively at the
// file level, so the supported flow is: copy the whole directory, then wipe. Wiping is the
// operation that has to be exhaustive, and VerifyDerivedStoresAbsent is what proves it was.
func WipeDerivedStores(db infrastructuredatabase.Database, prefixBytes []byte) error {
	// Raw infrastructure buckets here rather than the consensus wrappers: the consensus
	// dbBucket delegates to exactly this type with the same path bytes, and the Database
	// interface takes the raw ones.
	roots := []*infrastructuredatabase.Bucket{
		infrastructuredatabase.MakeBucket(prefixBytes),
		// The UTXO index lives outside the consensus prefix, at the top level of the datadir.
		infrastructuredatabase.MakeBucket(nil),
	}

	for _, root := range roots {
		for _, bucketName := range derivedStoreBuckets {
			if err := deleteBucket(db, root.Bucket(bucketName)); err != nil {
				return err
			}
			// Single-key stores live as a key directly under the root, not as a bucket.
			key := root.Key(bucketName)
			has, err := db.Has(key)
			if err != nil {
				return err
			}
			if has {
				if err := db.Delete(key); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func deleteBucket(db infrastructuredatabase.Database, bucket *infrastructuredatabase.Bucket) error {
	cursor, err := db.Cursor(bucket)
	if err != nil {
		return err
	}
	var keys []*infrastructuredatabase.Key
	for ok := cursor.First(); ok; ok = cursor.Next() {
		key, err := cursor.Key()
		if err != nil {
			cursor.Close()
			return err
		}
		// Copy the suffix. The underlying LevelDB iterator reuses one buffer for the key across
		// Next() calls, so a key retained past the next iteration is silently rewritten - which
		// makes the deletes land on the wrong keys and leaves the bucket looking half-wiped.
		suffix := append([]byte(nil), key.Suffix()...)
		keys = append(keys, bucket.Key(suffix))
	}
	cursor.Close()

	for _, key := range keys {
		if err := db.Delete(key); err != nil {
			return err
		}
	}
	return nil
}

// VerifyDerivedStoresAbsent fails if any derived store survived the wipe. Called after the
// copy so that a partial or skipped wipe cannot quietly hand the replay a poisoned input.
func VerifyDerivedStoresAbsent(db infrastructuredatabase.Database, prefixBytes []byte) error {
	roots := []*infrastructuredatabase.Bucket{
		infrastructuredatabase.MakeBucket(prefixBytes),
		infrastructuredatabase.MakeBucket(nil),
	}

	for _, root := range roots {
		for _, bucketName := range derivedStoreBuckets {
			cursor, err := db.Cursor(root.Bucket(bucketName))
			if err != nil {
				return err
			}
			hasAny := cursor.First()
			cursor.Close()
			if hasAny {
				return errors.Errorf("utxoderive: destination still contains derived store %q after the "+
					"wipe. Continuing would reintroduce the exported lineage this replay exists to "+
					"escape", bucketName)
			}

			has, err := db.Has(root.Key(bucketName))
			if err != nil {
				return err
			}
			if has {
				return errors.Errorf("utxoderive: destination still contains derived key %q after the "+
					"wipe", bucketName)
			}
		}
	}
	return nil
}

// PersistPruningPointUTXOSet writes the derived set into a destination datadir as the served
// pruning-point UTXO bucket, reusing the same import path a normal node uses so the on-disk
// shape is identical.
//
// Callers must only reach this after a walk whose derived MuHash matched the target block's
// header commitment. A set derived past an unresolved mismatch must never be persisted
// anywhere it could be served.
func PersistPruningPointUTXOSet(db infrastructuredatabase.Database, prefixBytes []byte,
	utxos map[externalapi.DomainOutpoint]externalapi.UTXOEntry, batchSize int,
) error {
	dbManager := consensusdatabase.New(db)
	prefixBucket := consensusdatabase.MakeBucket(prefixBytes)
	pruningStore := pruningstore.New(prefixBucket, 2, false)

	if err := pruningStore.ClearImportedPruningPointUTXOs(dbManager); err != nil {
		return err
	}

	batch := make([]*externalapi.OutpointAndUTXOEntryPair, 0, batchSize)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		dbTx, err := dbManager.Begin()
		if err != nil {
			return err
		}
		if err := pruningStore.AppendImportedPruningPointUTXOs(dbTx, batch); err != nil {
			dbTx.RollbackUnlessClosed()
			return err
		}
		if err := dbTx.Commit(); err != nil {
			return err
		}
		batch = batch[:0]
		return nil
	}

	for outpoint, entry := range utxos {
		outpointCopy := outpoint
		batch = append(batch, &externalapi.OutpointAndUTXOEntryPair{Outpoint: &outpointCopy, UTXOEntry: entry})
		if len(batch) >= batchSize {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	if err := flush(); err != nil {
		return err
	}

	return pruningStore.CommitImportedPruningPointUTXOSet(dbManager)
}
