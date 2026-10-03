package consensusstatestore

import (
	"bytes"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/database"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/utxo"
	"github.com/pkg/errors"
)

var utxoSetBucketName = []byte("virtual-utxo-set")

func (css *consensusStateStore) utxoKey(outpoint *externalapi.DomainOutpoint) (model.DBKey, error) {
	serializedOutpoint, err := serializeOutpoint(outpoint)
	if err != nil {
		return nil, err
	}

	return css.utxoSetBucket.Key(serializedOutpoint), nil
}

func (css *consensusStateStore) StageVirtualUTXODiff(stagingArea *model.StagingArea, virtualUTXODiff externalapi.UTXODiff) {
	stagingShard := css.stagingShard(stagingArea)

	stagingShard.virtualUTXODiffStaging = virtualUTXODiff
}

func (csss *consensusStateStagingShard) commitVirtualUTXODiff(dbTx model.DBTransaction) error {
	if csss.virtualUTXODiffStaging == nil {
		return nil
	}

	toRemoveIterator := csss.virtualUTXODiffStaging.ToRemove().Iterator()
	defer toRemoveIterator.Close()
	for ok := toRemoveIterator.First(); ok; ok = toRemoveIterator.Next() {
		toRemoveOutpoint, _, err := toRemoveIterator.Get()
		if err != nil {
			return err
		}

		csss.store.virtualUTXOSetCache.Remove(toRemoveOutpoint)

		dbKey, err := csss.store.utxoKey(toRemoveOutpoint)
		if err != nil {
			return err
		}
		err = dbTx.Delete(dbKey)
		if err != nil {
			return err
		}
	}

	toAddIterator := csss.virtualUTXODiffStaging.ToAdd().Iterator()
	defer toAddIterator.Close()
	for ok := toAddIterator.First(); ok; ok = toAddIterator.Next() {
		toAddOutpoint, toAddEntry, err := toAddIterator.Get()
		if err != nil {
			return err
		}

		csss.store.virtualUTXOSetCache.Add(toAddOutpoint, toAddEntry)

		dbKey, err := csss.store.utxoKey(toAddOutpoint)
		if err != nil {
			return err
		}
		serializedEntry, err := serializeUTXOEntry(toAddEntry)
		if err != nil {
			return err
		}
		err = dbTx.Put(dbKey, serializedEntry)
		if err != nil {
			return err
		}
	}

	// Note: we don't discard the staging here since that's
	// being done at the end of Commit()
	return nil
}

func (css *consensusStateStore) UTXOByOutpoint(dbContext model.DBReader, stagingArea *model.StagingArea,
	outpoint *externalapi.DomainOutpoint,
) (externalapi.UTXOEntry, bool, error) {
	stagingShard := css.stagingShard(stagingArea)

	return css.utxoByOutpointFromStagedVirtualUTXODiff(dbContext, stagingShard, outpoint, true)
}

// UTXOByOutpointWithoutPopulatingCache behaves like UTXOByOutpoint but never adds a cache miss to
// virtualUTXOSetCache - only a hit refreshes the entry's recency. For a bulk lookup whose outpoint
// count can dwarf the cache size (e.g. GetVirtualUTXOEntries answering an address-balance query),
// populating on every miss evicts entries block validation put there for its own working set, and
// buys the bulk lookup nothing back: it is walking a fixed list of outpoints it already has, not
// going to revisit the same key before it finishes. See HTN-207.
func (css *consensusStateStore) UTXOByOutpointWithoutPopulatingCache(dbContext model.DBReader,
	stagingArea *model.StagingArea, outpoint *externalapi.DomainOutpoint,
) (externalapi.UTXOEntry, bool, error) {
	stagingShard := css.stagingShard(stagingArea)

	return css.utxoByOutpointFromStagedVirtualUTXODiff(dbContext, stagingShard, outpoint, false)
}

// UTXOsByOutpointsWithoutPopulatingCache looks every outpoint up the way
// UTXOByOutpointWithoutPopulatingCache does, and sets entries[i] to the entry of outpoints[i], or
// leaves it nil where virtual's UTXO set does not hold the coin.
//
// The outpoints that are neither staged nor cached are read in key order through one cursor instead
// of one Get each. A Get starts a fresh descent of every level of the LSM tree for its key, and a
// wallet's coins are spread over the whole UTXO set, so a lookup of tens of thousands of them paid
// that descent tens of thousands of times. The keys are encoded once and the cursor is bounded to
// the range those keys cover, so each seek compares the bytes it already has.
func (css *consensusStateStore) UTXOsByOutpointsWithoutPopulatingCache(dbContext model.DBReader,
	stagingArea *model.StagingArea, outpoints []*externalapi.DomainOutpoint, entries []externalapi.UTXOEntry,
) error {
	if len(entries) != len(outpoints) {
		return errors.Errorf("%d entries given for %d outpoints", len(entries), len(outpoints))
	}
	ordered, err := css.PrepareOrderedVirtualUTXOKeys(outpoints)
	if err != nil {
		return err
	}
	return css.UTXOsByOrderedKeysWithoutPopulatingCache(dbContext, stagingArea, outpoints, ordered, entries, nil)
}

// UTXOsByOrderedKeysWithoutPopulatingCache fills entries from a key-sorted slice. See the
// interface comment for the preferred-entry reuse.
func (css *consensusStateStore) UTXOsByOrderedKeysWithoutPopulatingCache(dbContext model.DBReader,
	stagingArea *model.StagingArea, outpoints []*externalapi.DomainOutpoint, ordered []model.OrderedVirtualUTXOKey,
	entries []externalapi.UTXOEntry, preferred []externalapi.UTXOEntry,
) error {
	if len(entries) != len(outpoints) {
		return errors.Errorf("%d entries given for %d outpoints", len(entries), len(outpoints))
	}
	if preferred != nil && len(preferred) != len(outpoints) {
		return errors.Errorf("%d preferred entries given for %d outpoints", len(preferred), len(outpoints))
	}
	stagingShard := css.stagingShard(stagingArea)

	misses := make([]model.OrderedVirtualUTXOKey, 0, len(ordered))
	for _, item := range ordered {
		i := item.Index
		entries[i] = nil
		outpoint := outpoints[i]
		if stagingShard.virtualUTXODiffStaging != nil {
			if stagingShard.virtualUTXODiffStaging.ToRemove().Contains(outpoint) {
				continue
			}
			if utxoEntry, ok := stagingShard.virtualUTXODiffStaging.ToAdd().Get(outpoint); ok {
				entries[i] = utxoEntry
				continue
			}
		}
		if entry, ok := css.virtualUTXOSetCache.Get(outpoint); ok {
			entries[i] = entry
			continue
		}
		misses = append(misses, item)
	}
	if len(misses) == 0 {
		return nil
	}

	// misses is a subsequence of a key-sorted slice, so it is still sorted. Bounding the cursor
	// to that range lets the database skip sstables outside it. The bound is exclusive, so it
	// is the successor of the last key, clamped to the bucket.
	cursor, err := css.openBoundedUTXOCursor(dbContext, misses[0].Key, css.cursorUpper(misses[len(misses)-1].Key))
	if err != nil {
		return err
	}
	defer cursor.Close()

	var encoder utxoEntryEncoder
	for _, miss := range misses {
		err := cursor.SeekFullKey(miss.Key)
		if database.IsNotFoundError(err) {
			// Nothing remains at or after this key. Later keys are greater, so they are absent too,
			// but a bound can also exhaust the cursor between two misses, so keep walking.
			continue
		}
		if err != nil {
			return err
		}
		foundKey, err := cursor.FullKey()
		if database.IsNotFoundError(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !bytes.Equal(foundKey, miss.Key) {
			continue
		}
		serializedUTXOEntry, err := cursor.Value()
		if err != nil {
			return err
		}
		if preferred != nil && preferred[miss.Index] != nil {
			match, err := encoder.matches(preferred[miss.Index], serializedUTXOEntry)
			if err != nil {
				return err
			}
			if match {
				entries[miss.Index] = preferred[miss.Index]
				continue
			}
		}
		// deserializeUTXOEntry copies what it keeps, so the entry outlives the cursor's next move.
		entry, err := deserializeUTXOEntry(serializedUTXOEntry)
		if err != nil {
			return err
		}
		entries[miss.Index] = entry
	}
	return nil
}

// LookupUTXOByOutpoint answers what HasUTXOByOutpoint and UTXOByOutpoint answer together, in one
// lookup: the coin's entry and true, or nil and false where virtual's UTXO set does not hold it.
// Only a database fault is an error.
//
// Asking Has first cost every caller a database read even for a coin already in
// virtualUTXOSetCache, because Has never consults the cache, and two reads for a coin that is not.
// Like UTXOByOutpoint, it populates the cache on a miss.
func (css *consensusStateStore) LookupUTXOByOutpoint(dbContext model.DBReader, stagingArea *model.StagingArea,
	outpoint *externalapi.DomainOutpoint,
) (externalapi.UTXOEntry, bool, error) {
	return css.lookupUTXO(dbContext, css.stagingShard(stagingArea), outpoint, true)
}

// LookupUTXOByOutpointWithoutPopulatingCache is LookupUTXOByOutpoint for a bulk caller: it never adds a
// cache miss to virtualUTXOSetCache (see UTXOByOutpointWithoutPopulatingCache).
func (css *consensusStateStore) LookupUTXOByOutpointWithoutPopulatingCache(dbContext model.DBReader,
	stagingArea *model.StagingArea, outpoint *externalapi.DomainOutpoint,
) (externalapi.UTXOEntry, bool, error) {
	return css.lookupUTXO(dbContext, css.stagingShard(stagingArea), outpoint, false)
}

func (css *consensusStateStore) lookupUTXO(dbContext model.DBReader, stagingShard *consensusStateStagingShard,
	outpoint *externalapi.DomainOutpoint, populateCacheOnMiss bool,
) (externalapi.UTXOEntry, bool, error) {
	if stagingShard.virtualUTXODiffStaging != nil {
		if stagingShard.virtualUTXODiffStaging.ToRemove().Contains(outpoint) {
			return nil, false, nil
		}
		if utxoEntry, ok := stagingShard.virtualUTXODiffStaging.ToAdd().Get(outpoint); ok {
			return utxoEntry, true, nil
		}
	}

	if entry, ok := css.virtualUTXOSetCache.Get(outpoint); ok {
		return entry, true, nil
	}

	key, err := css.utxoKey(outpoint)
	if err != nil {
		return nil, false, err
	}

	serializedUTXOEntry, err := dbContext.Get(key)
	if database.IsNotFoundError(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}

	entry, err := deserializeUTXOEntry(serializedUTXOEntry)
	if err != nil {
		return nil, false, err
	}

	if populateCacheOnMiss {
		css.virtualUTXOSetCache.Add(outpoint, entry)
	}
	return entry, true, nil
}

func (css *consensusStateStore) utxoByOutpointFromStagedVirtualUTXODiff(dbContext model.DBReader,
	stagingShard *consensusStateStagingShard, outpoint *externalapi.DomainOutpoint, populateCacheOnMiss bool,
) (externalapi.UTXOEntry, bool, error) {
	if stagingShard.virtualUTXODiffStaging != nil && stagingShard.virtualUTXODiffStaging.ToRemove().Contains(outpoint) {
		return nil, false, errors.Errorf("outpoint was not found")
	}
	entry, found, err := css.lookupUTXO(dbContext, stagingShard, outpoint, populateCacheOnMiss)
	if err != nil {
		return nil, false, err
	}
	if !found {
		return nil, false, errors.Wrapf(database.ErrNotFound, "UTXO entry %s does not exist in db", outpoint)
	}
	return entry, true, nil
}

func (css *consensusStateStore) HasUTXOByOutpoint(dbContext model.DBReader, stagingArea *model.StagingArea,
	outpoint *externalapi.DomainOutpoint,
) (bool, error) {
	stagingShard := css.stagingShard(stagingArea)

	return css.hasUTXOByOutpointFromStagedVirtualUTXODiff(dbContext, stagingShard, outpoint)
}

func (css *consensusStateStore) hasUTXOByOutpointFromStagedVirtualUTXODiff(dbContext model.DBReader,
	stagingShard *consensusStateStagingShard, outpoint *externalapi.DomainOutpoint,
) (bool, error) {
	if stagingShard.virtualUTXODiffStaging != nil {
		if stagingShard.virtualUTXODiffStaging.ToRemove().Contains(outpoint) {
			return false, nil
		}
		if _, ok := stagingShard.virtualUTXODiffStaging.ToAdd().Get(outpoint); ok {
			return true, nil
		}
	}

	key, err := css.utxoKey(outpoint)
	if err != nil {
		return false, err
	}

	return dbContext.Has(key)
}

func (css *consensusStateStore) VirtualUTXOs(dbContext model.DBReader, fromOutpoint *externalapi.DomainOutpoint, limit int) (
	[]*externalapi.OutpointAndUTXOEntryPair, error,
) {
	cursor, err := dbContext.Cursor(css.utxoSetBucket)
	if err != nil {
		return nil, err
	}
	defer cursor.Close()

	if fromOutpoint != nil {
		serializedFromOutpoint, err := serializeOutpoint(fromOutpoint)
		if err != nil {
			return nil, err
		}
		seekKey := css.utxoSetBucket.Key(serializedFromOutpoint)
		err = cursor.Seek(seekKey)
		if err != nil {
			log.Infof("Cursor seek failed at serialized outpoint key %s\n", serializedFromOutpoint)
			return nil, err
		}
	}

	iterator := newCursorUTXOSetIterator(cursor)
	defer iterator.Close()

	outpointAndUTXOEntryPairs := make([]*externalapi.OutpointAndUTXOEntryPair, 0, limit)
	for len(outpointAndUTXOEntryPairs) < limit && iterator.Next() {
		outpoint, utxoEntry, err := iterator.Get()
		if err != nil {
			return nil, err
		}
		outpointAndUTXOEntryPairs = append(outpointAndUTXOEntryPairs, &externalapi.OutpointAndUTXOEntryPair{
			Outpoint:  outpoint,
			UTXOEntry: utxoEntry,
		})
	}
	return outpointAndUTXOEntryPairs, nil
}

func (css *consensusStateStore) VirtualUTXOSetIterator(dbContext model.DBReader, stagingArea *model.StagingArea) (
	externalapi.ReadOnlyUTXOSetIterator, error,
) {
	stagingShard := css.stagingShard(stagingArea)

	cursor, err := dbContext.Cursor(css.utxoSetBucket)
	if err != nil {
		return nil, err
	}

	mainIterator := newCursorUTXOSetIterator(cursor)
	if stagingShard.virtualUTXODiffStaging != nil {
		return utxo.IteratorWithDiff(mainIterator, stagingShard.virtualUTXODiffStaging)
	}

	return mainIterator, nil
}

type utxoSetIterator struct {
	cursor   model.DBCursor
	isClosed bool
}

func newCursorUTXOSetIterator(cursor model.DBCursor) externalapi.ReadOnlyUTXOSetIterator {
	return &utxoSetIterator{cursor: cursor}
}

func (u *utxoSetIterator) First() bool {
	if u.isClosed {
		panic("Tried using a closed utxoSetIterator")
	}
	return u.cursor.First()
}

func (u *utxoSetIterator) Next() bool {
	if u.isClosed {
		panic("Tried using a closed utxoSetIterator")
	}
	return u.cursor.Next()
}

func (u *utxoSetIterator) Get() (outpoint *externalapi.DomainOutpoint, utxoEntry externalapi.UTXOEntry, err error) {
	if u.isClosed {
		return nil, nil, errors.New("Tried using a closed utxoSetIterator")
	}
	key, err := u.cursor.Key()
	if err != nil {
		panic(err)
	}

	utxoEntryBytes, err := u.cursor.Value()
	if err != nil {
		return nil, nil, err
	}

	outpoint, err = deserializeOutpoint(key.Suffix())
	if err != nil {
		return nil, nil, err
	}

	utxoEntry, err = deserializeUTXOEntry(utxoEntryBytes)
	if err != nil {
		return nil, nil, err
	}

	return outpoint, utxoEntry, nil
}

func (u *utxoSetIterator) Close() error {
	if u.isClosed {
		return errors.New("Tried using a closed utxoSetIterator")
	}
	u.isClosed = true
	err := u.cursor.Close()
	if err != nil {
		return err
	}
	u.cursor = nil
	return nil
}
