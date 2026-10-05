package database

// Cursor iterates over database entries given some bucket.
type Cursor interface {
	// Next moves the iterator to the next key/value pair. It returns whether the
	// iterator is exhausted. Panics if the cursor is closed.
	Next() bool

	// First moves the iterator to the first key/value pair. It returns false if
	// such a pair does not exist. Panics if the cursor is closed.
	First() bool

	// Seek moves the iterator to the first key/value pair whose key is greater
	// than or equal to the given key. It returns ErrNotFound if such pair does not
	// exist.
	Seek(key *Key) error

	// SeekFullKey moves the iterator to the first key greater than or equal to key.
	// key is the full database key, bucket prefix included. ErrNotFound means nothing
	// remains at or after key. A missing key that still has a successor is not an error:
	// the cursor sits on that successor, and FullKey reports it. The LevelDB Seek method
	// is stricter than this and reports a missing key as ErrNotFound.
	SeekFullKey(key []byte) error

	// Key returns the key of the current key/value pair, or ErrNotFound if done.
	// The caller should not modify the contents of the returned key, and
	// its contents may change on the next call to Next.
	Key() (*Key, error)

	// FullKey returns the current full key, bucket prefix included, or ErrNotFound if
	// the cursor is exhausted. The caller should not modify the returned slice. Its
	// contents may change on the next cursor movement, and it is not valid after that.
	FullKey() ([]byte, error)

	// Value returns the value of the current key/value pair, or ErrNotFound if done.
	// The caller should not modify the contents of the returned slice, and its
	// contents may change on the next call to Next.
	Value() ([]byte, error)

	// Close releases associated resources.
	Close() error
}

// BoundCursorOpener opens a cursor over bucket limited to keys in [lower, upper).
// A nil bound leaves that side at the bucket's own prefix range. lower and upper are
// full keys and must not be modified while the cursor is open.
type BoundCursorOpener interface {
	CursorBounds(bucket *Bucket, lower, upper []byte) (Cursor, error)
}
