package database

import (
	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/infrastructure/db/database"
	"github.com/HoosatNetwork/HTND/infrastructure/logger"
)

// Database retry constants removed - database operations should be fast and reliable
// without retries that introduce unacceptable latency in high-throughput scenarios.

var log = logger.RegisterSubSystem("DBMG")

type dbManager struct {
	db database.Database
}

func (dbw *dbManager) Get(key model.DBKey) ([]byte, error) {
	data, err := dbw.db.Get(dbKeyToDatabaseKey(key))
	if err != nil {
		return nil, err
	}
	return data, nil
}

func (dbw *dbManager) Has(key model.DBKey) (bool, error) {
	return dbw.db.Has(dbKeyToDatabaseKey(key))
}

func (dbw *dbManager) Put(key model.DBKey, value []byte) error {
	return dbw.db.Put(dbKeyToDatabaseKey(key), value)
}

func (dbw *dbManager) Delete(key model.DBKey) error {
	return dbw.db.Delete(dbKeyToDatabaseKey(key))
}

func (dbw *dbManager) Cursor(bucket model.DBBucket) (model.DBCursor, error) {
	cursor, err := dbw.db.Cursor(dbBucketToDatabaseBucket(bucket))
	if err != nil {
		return nil, err
	}

	return newDBCursor(cursor), nil
}

func (dbw *dbManager) Begin() (model.DBTransaction, error) {
	transaction, err := dbw.db.Begin()
	if err != nil {
		return nil, err
	}
	return newDBTransaction(transaction), nil
}

// New returns wraps the given database as an instance of model.DBManager
func New(db database.Database) model.DBManager {
	return &dbManager{db: db}
}
