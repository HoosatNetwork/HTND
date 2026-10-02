package consensusstatestore

import (
	"bytes"
	"slices"

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
// that descent tens of thousands of times. Seeking one iterator forward lets pebble continue from
// where the previous seek left each level (TrySeekUsingNext) and reuse the blocks it has already
// loaded.
func (css *consensusStateStore) UTXOsByOutpointsWithoutPopulatingCache(dbContext model.DBReader,
	stagingArea *model.StagingArea, outpoints []*externalapi.DomainOutpoint, entries []externalapi.UTXOEntry,
) error {
	if len(entries) != len(outpoints) {
		return errors.Errorf("%d entries given for %d outpoints", len(entries), len(outpoints))
	}
	stagingShard := css.stagingShard(stagingArea)

	type dbLookup struct {
		index int
		key   model.DBKey
	}
	var lookups []dbLookup
	for i, outpoint := range outpoints {
		entries[i] = nil
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
		key, err := css.utxoKey(outpoint)
		if err != nil {
			return err
		}
		lookups = append(lookups, dbLookup{index: i, key: key})
	}
	if len(lookups) == 0 {
		return nil
	}

	slices.SortFunc(lookups, func(a, b dbLookup) int {
		return bytes.Compare(a.key.Bytes(), b.key.Bytes())
	})

	cursor, err := dbContext.Cursor(css.utxoSetBucket)
	if err != nil {
		return err
	}
	defer cursor.Close()

	for _, lookup := range lookups {
		err := cursor.Seek(lookup.key)
		if database.IsNotFoundError(err) {
			// Pebble reports this only once nothing is left at or after the key, but the LevelDB cursor
			// also reports it whenever the exact key is missing, so it cannot end the walk.
			continue
		}
		if err != nil {
			return err
		}
		foundKey, err := cursor.Key()
		if err != nil {
			return err
		}
		if !bytes.Equal(foundKey.Bytes(), lookup.key.Bytes()) {
			continue
		}
		serializedUTXOEntry, err := cursor.Value()
		if err != nil {
			return err
		}
		// deserializeUTXOEntry copies what it keeps, so the entry outlives the cursor's next move.
		entry, err := deserializeUTXOEntry(serializedUTXOEntry)
		if err != nil {
			return err
		}
		entries[lookup.index] = entry
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
