package pebble

import (
	"bytes"
	"context"
	"os"
	"sync"

	"github.com/Hoosat-Oy/HTND/infrastructure/db/database"
	"github.com/cockroachdb/pebble/v2"
	"github.com/pkg/errors"
)

// PebbleDB defines a thin wrapper around Pebble.
type PebbleDB struct {
	db      *pebble.DB
	cursors []*PebbleDBCursor // Track all cursors
	mu      sync.Mutex        // Protect cursors slice
}

// NewPebbleDB opens a Pebble instance defined by the given path.
func NewPebbleDB(path string, cacheSizeMiB int) (*PebbleDB, error) {
	options := Options(cacheSizeMiB)

	db, err := pebble.Open(path, options)
	if err != nil {
		if errors.Is(err, pebble.ErrCorruption) {
			log.Warnf("Pebble corruption detected at %s: %v", path, err)

			// Remove the corrupted DB
			log.Warnf("Removing corrupted DB at %s", path)
			if rmErr := os.RemoveAll(path); rmErr != nil {
				return nil, errors.Wrap(rmErr, "failed to remove corrupted DB")
			}

			// Attempt to create a fresh DB
			db, err = pebble.Open(path, options)
			if err != nil {
				return nil, errors.Wrap(err, "failed to create fresh DB after corruption")
			}
			log.Warnf("Created fresh Pebble DB at %s", path)
		} else {
			return nil, errors.WithStack(err)
		}
	}

	dbInstance := &PebbleDB{
		db: db,
	}
	return dbInstance, nil
}

// Compact compacts the Pebble instance (full range).
func (db *PebbleDB) Compact() error {
	// Full-range compaction: empty start key to max end key, non-parallel
	err := db.db.Compact(context.Background(), nil, []byte{0xff, 0xff, 0xff, 0xff}, false)
	return errors.WithStack(err)
}

// Close closes the Pebble instance and all associated cursors.
func (db *PebbleDB) Close() error {
	// Close all tracked cursors
	for _, cursor := range db.cursors {
		if !cursor.isClosed {
			if err := cursor.Close(); err != nil {
				log.Warnf("Failed to close cursor: %v", err)
			}
		}
	}
	db.cursors = nil // Clear cursors

	// Close the database
	err := db.db.Close()
	return errors.WithStack(err)
}

// Put sets the value for the given key. It overwrites any previous value for that key.
func (db *PebbleDB) Put(key *database.Key, value []byte) error {
	// log.Infof("Put key: %s, value %x", key, value)
	err := db.db.Set(key.Bytes(), value, pebble.NoSync)
	return errors.WithStack(err)
}

func (db *PebbleDB) BatchPut(pairs map[*database.Key][]byte) error {
	batch := db.db.NewBatch()
	defer batch.Close()
	for key, value := range pairs {
		if err := batch.Set(key.Bytes(), value, pebble.NoSync); err != nil {
			return errors.Wrapf(err, "failed to set key %s in batch", key)
		}
	}
	if err := batch.Commit(pebble.NoSync); err != nil {
		return errors.WithStack(err)
	}
	return nil
}

// Get gets the value for the given key. It returns ErrNotFound if the given key does not exist.
func (db *PebbleDB) Get(key *database.Key) ([]byte, error) {
	data, closer, err := db.db.Get(key.Bytes())
	if err != nil {
		if errors.Is(err, pebble.ErrNotFound) {
			return nil, errors.Wrapf(database.ErrNotFound, "key %s not found", key)
		}
		return nil, errors.WithStack(err)
	}
	valueCopy := bytes.Clone(data)
	closer.Close()
	return valueCopy, nil
}

// Has returns true if the database contains the given key.
func (db *PebbleDB) Has(key *database.Key) (bool, error) {
	_, closer, err := db.db.Get(key.Bytes())
	if err != nil {
		if errors.Is(err, pebble.ErrNotFound) {
			return false, nil
		}
		return false, errors.WithStack(err)
	}
	closer.Close()
	return true, nil
}

// Delete deletes the value for the given key. Will not return an error if the key doesn't exist.
func (db *PebbleDB) Delete(key *database.Key) error {
	err := db.db.Delete(key.Bytes(), pebble.NoSync)
	return errors.WithStack(err)
}

// registerCursor registers a cursor with the database for tracking.
func (db *PebbleDB) registerCursor(cursor *PebbleDBCursor) {
	db.mu.Lock()
	defer db.mu.Unlock()
	db.cursors = append(db.cursors, cursor)
}

// deregisterCursor removes a cursor from the database's tracking.
func (db *PebbleDB) deregisterCursor(cursor *PebbleDBCursor) {
	db.mu.Lock()
	defer db.mu.Unlock()
	for i, c := range db.cursors {
		if c == cursor {
			db.cursors = append(db.cursors[:i], db.cursors[i+1:]...)
			break
		}
	}
}
