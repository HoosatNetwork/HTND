package pebble

import (
	"bytes"

	"github.com/Hoosat-Oy/HTND/infrastructure/db/database"
	"github.com/cockroachdb/pebble/v2"
	"github.com/pkg/errors"
)

// DBCursor is a thin wrapper around Pebble iterators.
type DBCursor struct {
	db          *DB
	iterator    *pebble.Iterator
	firstCalled bool
	bucket      *database.Bucket
	isClosed    bool
}

// BytesPrefix returns iterator options for keys with the given prefix, with a computed upper bound.
func BytesPrefix(prefix []byte) *pebble.IterOptions {
	var limit []byte
	for i := len(prefix) - 1; i >= 0; i-- {
		c := prefix[i]
		if c < 0xff {
			limit = make([]byte, i+1)
			copy(limit, prefix)
			limit[i] = c + 1
			break
		}
	}
	// log.Infof("BytesPrefix: prefix=%x, limit=%x", prefix, limit)
	return &pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: limit,
		// Optional v2 fields: Set if needed (e.g., TableFilter for skipping files)
		// TableFilter: func(level int, fileNum uint64) bool { return true }, // Example: Include all
		// OnlyReadGuaranteedDurable: true, // For crash consistency in IBD
	}
}

// Cursor begins a new cursor over the given prefix.
func (db *DB) Cursor(bucket *database.Bucket) (database.Cursor, error) {
	// log.Infof("Bucket path = %x", bucket.Path())
	// log.Infof("Opening cursor for bucket path: %x", bucket.Path())
	iterator, err := db.db.NewIter(BytesPrefix(bucket.Path()))
	if err != nil {
		return nil, errors.Wrap(err, "failed to create iterator")
	}
	cursor := &DBCursor{
		db:          db,
		iterator:    iterator,
		bucket:      bucket,
		isClosed:    false,
		firstCalled: false,
	}
	db.registerCursor(cursor) // Register cursor with database
	return cursor, nil
}

// Next moves the iterator to the next key/value pair. It returns whether the iterator is exhausted.
// Panics if the cursor is closed.
func (c *DBCursor) Next() bool {
	if c.isClosed {
		panic("cannot call next on a closed cursor")
	}
	if !c.firstCalled {
		hasNext := c.iterator.First()
		// log.Infof("First: hasNext=%v, valid=%v, key=%x", hasNext, c.iterator.Valid(), c.iterator.Key())
		c.firstCalled = true
		return hasNext
	}
	// log.Infof("Before Next: valid=%v, key=%x", c.iterator.Valid(), c.iterator.Key())
	hasNext := c.iterator.Next()
	// log.Infof("After Next: hasNext=%v, valid=%v, key=%x", hasNext, c.iterator.Valid(), c.iterator.Key())
	return hasNext
}

// First moves the iterator to the first key/value pair. It returns false if such a pair does not exist.
// Panics if the cursor is closed.
func (c *DBCursor) First() bool {
	if c.isClosed {
		panic("cannot call First on a closed cursor")
	}
	hasFirst := c.iterator.First()
	c.firstCalled = true
	// log.Infof("First: hasFirst=%v, currentKey=%x", hasFirst, c.iterator.Key())
	return hasFirst
}

// Seek moves the iterator to the first key/value pair whose key is greater
// than or equal to the given key. It returns ErrNotFound if such pair does not exist.
func (c *DBCursor) Seek(key *database.Key) error {
	if c.isClosed {
		return errors.New("cannot seek a closed cursor")
	}
	found := c.iterator.SeekGE(key.Bytes())
	c.firstCalled = true
	// log.Infof("Seek %s, found: %t", key.Bytes(), found)
	if !found {
		return errors.Wrapf(database.ErrNotFound, "no key found for seek %s", key)
	}
	return nil
}

// Key returns the key of the current key/value pair, or ErrNotFound if done.
// Note that the key is trimmed to not include the prefix the cursor was opened with.
// The caller should not modify the contents of the returned slice, and its contents may change
// on the next call to Next.
func (c *DBCursor) Key() (*database.Key, error) {
	if c.isClosed {
		return nil, errors.New("cannot get the key of a closed cursor")
	}
	if !c.iterator.Valid() {
		if iterErr := c.iterator.Error(); iterErr != nil {
			return nil, errors.Wrap(iterErr, "iterator error")
		}
		return nil, errors.Wrapf(database.ErrNotFound, "cannot get the key of an exhausted cursor")
	}
	fullKeyPath := c.iterator.Key()
	if fullKeyPath == nil {
		return nil, errors.Wrapf(database.ErrNotFound, "cannot get the key of an exhausted cursor")
	}
	suffix := bytes.TrimPrefix(fullKeyPath, c.bucket.Path())
	// log.Infof("Key: fullKeyPath=%x, suffix=%x", fullKeyPath, suffix)
	return c.bucket.Key(suffix), nil
}

// Value returns the value of the current key/value pair, or ErrNotFound if done.
// The caller should not modify the contents of the returned slice, and its contents may change
// on the next call to Next.
func (c *DBCursor) Value() ([]byte, error) {
	if c.isClosed {
		return nil, errors.New("cannot get the value of a closed cursor")
	}
	if !c.iterator.Valid() {
		if iterErr := c.iterator.Error(); iterErr != nil {
			return nil, errors.Wrap(iterErr, "iterator error")
		}
		return nil, errors.Wrapf(database.ErrNotFound, "cannot get the value of an exhausted cursor")
	}
	value := c.iterator.Value()
	if value == nil {
		return nil, errors.Wrapf(database.ErrNotFound, "cannot get the value of an exhausted cursor")
	}
	// log.Infof("Value: value=%x", value)
	return value, nil
}

// Close releases associated resources.
func (c *DBCursor) Close() error {
	if c.isClosed {
		return errors.New("cannot close an already closed cursor")
	}
	c.firstCalled = false
	c.isClosed = true
	c.db.deregisterCursor(c) // Deregister from database
	err := c.iterator.Close()
	c.iterator = nil
	c.bucket = nil
	return errors.WithStack(err)
}
