package database

import (
	"time"

	"github.com/Hoosat-Oy/HTND/domain/consensus/model"
	"github.com/Hoosat-Oy/HTND/infrastructure/db/database"
	"github.com/Hoosat-Oy/HTND/infrastructure/logger"
)

const (
	// Retry configuration for Get operations when returning empty values
	maxGetRetryAttempts = 3
	initialRetryDelay   = 10 * time.Millisecond
	maxRetryDelay       = 500 * time.Millisecond
)

var log = logger.RegisterSubSystem("DBMG")

type dbManager struct {
	db database.Database
}

func (dbw *dbManager) Get(key model.DBKey) ([]byte, error) {
	databaseKey := dbKeyToDatabaseKey(key)
	retryDelay := initialRetryDelay

	for attempt := range maxGetRetryAttempts {
		data, err := dbw.db.Get(databaseKey)

		// If there was an error, return it immediately (no retry for errors)
		if err != nil {
			return nil, err
		}

		// If we got data (non-empty), return it
		if len(data) > 0 {
			if attempt > 0 {
				log.Debugf("Successfully retrieved data for key %s after %d retry attempts", key, attempt)
			}
			return data, nil
		}

		// If this is the last attempt, return the empty data
		if attempt == maxGetRetryAttempts-1 {
			return data, nil
		}

		// Empty data returned, retry after delay
		log.Debugf("Empty data returned for key %s on attempt %d/%d, retrying after %v",
			key, attempt+1, maxGetRetryAttempts, retryDelay)
		time.Sleep(retryDelay)

		// Exponential backoff with maximum cap
		retryDelay *= 2
		if retryDelay > maxRetryDelay {
			retryDelay = maxRetryDelay
		}
	}

	// This line should never be reached, but included for safety
	return dbw.db.Get(databaseKey)
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
