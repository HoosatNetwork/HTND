package ldb

import (
	"os"
	"strings"

	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database"
	"github.com/pkg/errors"
	"github.com/syndtr/goleveldb/leveldb"
	ldbErrors "github.com/syndtr/goleveldb/leveldb/errors"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/util"
)

// LevelDB defines a thin wrapper around leveldb.
type LevelDB struct {
	ldb *leveldb.DB
}

// isPebbleDirectory reports whether path holds a pebble database. Pebble always writes OPTIONS-<n> files and a
// marker.format-version file; goleveldb never creates either.
func isPebbleDirectory(path string) (bool, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, errors.WithStack(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "OPTIONS-") || strings.HasPrefix(entry.Name(), "marker.") {
			return true, nil
		}
	}
	return false, nil
}

// NewLevelDB opens a leveldb instance defined by the given path.
func NewLevelDB(path string, cacheSizeMiB int) (*LevelDB, error) {
	// Refuse a pebble datadir before goleveldb touches it. goleveldb reports it as corrupted, and the recovery
	// below then rebuilds a LevelDB manifest in place, destroying the pebble database - ldbtool fuse/copy and
	// --dbtype=leveldb reach this with whatever path they are given, including copy sources.
	isPebble, err := isPebbleDirectory(path)
	if err != nil {
		return nil, err
	}
	if isPebble {
		return nil, errors.Errorf("%s is a pebble database; refusing to open it with LevelDB, which would destroy it", path)
	}

	// Open leveldb. If it doesn't exist, create it.
	options := Options()
	if cacheSizeMiB > 0 {
		options.BlockCacheCapacity = cacheSizeMiB * opt.MiB
		options.WriteBuffer = (cacheSizeMiB * opt.MiB) / 2
	}
	ldb, err := leveldb.OpenFile(path, &options)

	// If the database is corrupted, attempt to recover.
	if _, corrupted := err.(*ldbErrors.ErrCorrupted); corrupted {
		log.Warnf("LevelDB corruption detected for path %s: %s",
			path, err)
		var recoverErr error
		log.Warnf("Starting to recover LevelDB for path %s: %s", path, err)
		ldb, recoverErr = leveldb.RecoverFile(path, nil)
		if recoverErr != nil {
			return nil, errors.Wrapf(err, "failed recovering from "+
				"database corruption: %s", recoverErr)
		}
		log.Warnf("LevelDB recovered from corruption for path %s",
			path)
		// Reset original open error after successful recovery so we don't report it below.
		err = nil
	}

	// If the database cannot be opened for any other
	// reason, return the error as-is.
	if err != nil {
		return nil, errors.WithStack(err)
	}

	db := &LevelDB{
		ldb: ldb,
	}
	return db, nil
}

// Compact compacts the leveldb instance.
func (db *LevelDB) Compact() error {
	err := db.ldb.CompactRange(util.Range{Start: nil, Limit: nil})
	return errors.WithStack(err)
}

// Close closes the leveldb instance.
func (db *LevelDB) Close() error {
	err := db.ldb.Close()
	return errors.WithStack(err)
}

// Put sets the value for the given key. It overwrites
// any previous value for that key.
func (db *LevelDB) Put(key *database.Key, value []byte) error {
	err := db.ldb.Put(key.Bytes(), value, nil)
	return errors.WithStack(err)
}

func (db *LevelDB) BatchPut(pairs map[*database.Key][]byte) error {
	batch := new(leveldb.Batch)
	defer batch.Reset()

	for key, value := range pairs {
		batch.Put(key.Bytes(), value)
	}

	if err := db.ldb.Write(batch, nil); err != nil {
		return errors.WithStack(err)
	}
	return nil
}

// Get gets the value for the given key. It returns
// ErrNotFound if the given key does not exist.
func (db *LevelDB) Get(key *database.Key) ([]byte, error) {
	data, err := db.ldb.Get(key.Bytes(), nil)
	if err != nil {
		if errors.Is(err, leveldb.ErrNotFound) {
			return nil, errors.Wrapf(database.ErrNotFound,
				"key %s not found", key)
		}
		return nil, errors.WithStack(err)
	}
	return data, nil
}

// Has returns true if the database does contains the
// given key.
func (db *LevelDB) Has(key *database.Key) (bool, error) {
	exists, err := db.ldb.Has(key.Bytes(), nil)
	if err != nil {
		return false, errors.WithStack(err)
	}
	return exists, nil
}

// Delete deletes the value for the given key. Will not
// return an error if the key doesn't exist.
func (db *LevelDB) Delete(key *database.Key) error {
	err := db.ldb.Delete(key.Bytes(), nil)
	return errors.WithStack(err)
}
