package pebble

import (
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database"
	"github.com/cockroachdb/pebble/v2"
	"github.com/pkg/errors"
)

var _ database.Snapshotter = (*DB)(nil)
var _ database.Snapshot = (*snapshot)(nil)

type snapshot struct {
	owner   *DB
	view    *pebble.Snapshot
	cursors int
}

// NewSnapshot pins reads only. WAL and disk admission belong to any future
// durable worker, not this generic read capability.
func (db *DB) NewSnapshot() (database.Snapshot, error) {
	return &snapshot{owner: db, view: db.db.NewSnapshot()}, nil
}
func (s *snapshot) Get(k *database.Key) ([]byte, error) {
	if s.view == nil {
		return nil, database.ErrSnapshotClosed
	}
	v, c, err := s.view.Get(k.Bytes())
	if errors.Is(err, pebble.ErrNotFound) {
		return nil, database.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	copyV := append([]byte(nil), v...)
	return copyV, c.Close()
}
func (s *snapshot) Has(k *database.Key) (bool, error) {
	_, err := s.Get(k)
	if errors.Is(err, database.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}
func (s *snapshot) Cursor(b *database.Bucket) (database.Cursor, error) {
	if s.view == nil {
		return nil, database.ErrSnapshotClosed
	}
	it, err := s.view.NewIter(BytesPrefix(b.Path()))
	if err != nil {
		return nil, err
	}
	c := &DBCursor{db: s.owner, iterator: it, bucket: b}
	s.owner.registerCursor(c)
	s.cursors++
	return &snapshotCursor{DBCursor: c, snapshot: s}, nil
}
func (s *snapshot) Close() error {
	if s.view == nil {
		return database.ErrSnapshotClosed
	}
	if s.cursors != 0 {
		return database.ErrSnapshotCursorsOpen
	}
	err := s.view.Close()
	s.view = nil
	return err
}

type snapshotCursor struct {
	*DBCursor
	snapshot *snapshot
}

func (c *snapshotCursor) Close() error {
	wasClosed := c.isClosed
	err := c.DBCursor.Close()
	if !wasClosed {
		c.snapshot.cursors--
	}
	return err
}
