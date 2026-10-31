package database

import (
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	db "github.com/HoosatNetwork/HTND/v2/infrastructure/db/database"
	"github.com/pkg/errors"
)

var (
	ErrSnapshotUnsupported = errors.New("backend does not support snapshots")
	ErrReadOnlySnapshot    = errors.New("read-only snapshot")
)

var _ model.DBManager = (*ReadSnapshot)(nil)

// ReadSnapshot rejects writes and transaction creation. Stores and their caches
// still have to be private: using live stores with this reader is NOT isolated.
// Its lifetime and cursor ownership follow the underlying database.Snapshot.
type ReadSnapshot struct{ view db.Snapshot }

func (d *dbManager) NewSnapshot() (*ReadSnapshot, error) {
	s, ok := d.db.(db.Snapshotter)
	if !ok {
		return nil, ErrSnapshotUnsupported
	}
	v, err := s.NewSnapshot()
	if err != nil {
		return nil, err
	}
	return &ReadSnapshot{v}, nil
}
func (s *ReadSnapshot) Get(k model.DBKey) ([]byte, error) { return s.view.Get(dbKeyToDatabaseKey(k)) }
func (s *ReadSnapshot) Has(k model.DBKey) (bool, error)   { return s.view.Has(dbKeyToDatabaseKey(k)) }
func (s *ReadSnapshot) Cursor(b model.DBBucket) (model.DBCursor, error) {
	c, e := s.view.Cursor(dbBucketToDatabaseBucket(b))
	if e != nil {
		return nil, e
	}
	return newDBCursor(c), nil
}
func (s *ReadSnapshot) Close() error                  { return s.view.Close() }
func (s *ReadSnapshot) Put(model.DBKey, []byte) error { return ErrReadOnlySnapshot }
func (s *ReadSnapshot) Delete(model.DBKey) error      { return ErrReadOnlySnapshot }
func (s *ReadSnapshot) Begin() (model.DBTransaction, error) {
	return nil, ErrReadOnlySnapshot
}
func (s *ReadSnapshot) BeginUnindexed() (model.DBTransaction, error) { return s.Begin() }
