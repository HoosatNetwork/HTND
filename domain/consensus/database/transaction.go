package database

import (
	"time"

	"github.com/Hoosat-Oy/HTND/domain/consensus/model"
	"github.com/Hoosat-Oy/HTND/infrastructure/db/database"
)

type dbTransaction struct {
	transaction database.Transaction
}

func (d *dbTransaction) Get(key model.DBKey) ([]byte, error) {
	databaseKey := dbKeyToDatabaseKey(key)
	retryDelay := initialRetryDelay

	for attempt := range maxGetRetryAttempts {
		data, err := d.transaction.Get(databaseKey)

		// If there was an error, return it immediately (no retry for errors)
		if err != nil {
			return nil, err
		}

		// If we got data (non-empty), return it
		if len(data) > 0 {
			if attempt > 0 {
				log.Debugf("Successfully retrieved data for key %s after %d retry attempts (transaction)", key, attempt)
			}
			return data, nil
		}

		// If this is the last attempt, return the empty data
		if attempt == maxGetRetryAttempts-1 {
			return data, nil
		}

		// Empty data returned, retry after delay
		log.Debugf("Empty data returned for key %s on attempt %d/%d, retrying after %v (transaction)",
			key, attempt+1, maxGetRetryAttempts, retryDelay)
		time.Sleep(retryDelay)

		// Exponential backoff with maximum cap
		retryDelay *= 2
		if retryDelay > maxRetryDelay {
			retryDelay = maxRetryDelay
		}
	}

	// This line should never be reached, but included for safety
	return d.transaction.Get(databaseKey)
}

func (d *dbTransaction) Has(key model.DBKey) (bool, error) {
	return d.transaction.Has(dbKeyToDatabaseKey(key))
}

func (d *dbTransaction) Cursor(bucket model.DBBucket) (model.DBCursor, error) {
	cursor, err := d.transaction.Cursor(dbBucketToDatabaseBucket(bucket))
	if err != nil {
		return nil, err
	}
	return newDBCursor(cursor), nil
}

func (d *dbTransaction) Put(key model.DBKey, value []byte) error {
	return d.transaction.Put(dbKeyToDatabaseKey(key), value)
}

func (d *dbTransaction) Delete(key model.DBKey) error {
	return d.transaction.Delete(dbKeyToDatabaseKey(key))
}

func (d *dbTransaction) Rollback() error {
	return d.transaction.Rollback()
}

func (d *dbTransaction) Commit() error {
	return d.transaction.Commit()
}

func (d *dbTransaction) RollbackUnlessClosed() error {
	return d.transaction.RollbackUnlessClosed()
}

func newDBTransaction(transaction database.Transaction) model.DBTransaction {
	return &dbTransaction{transaction: transaction}
}
