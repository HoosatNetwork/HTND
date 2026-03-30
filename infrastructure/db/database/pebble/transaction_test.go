package pebble

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Hoosat-Oy/HTND/infrastructure/db/database"
)

func TestTransactionCloseErrors(t *testing.T) {
	tests := []struct {
		name              string
		function          func(dbTx *PebbleDBTransaction) error
		shouldReturnError bool
	}{
		{
			name: "Put",
			function: func(dbTx *PebbleDBTransaction) error {
				return dbTx.Put(database.MakeBucket(nil).Key([]byte("key")), []byte("value"))
			},
			shouldReturnError: true,
		},
		{
			name: "Get",
			function: func(dbTx *PebbleDBTransaction) error {
				_, err := dbTx.Get(database.MakeBucket(nil).Key([]byte("key")))
				return err
			},
			shouldReturnError: true,
		},
		{
			name: "Has",
			function: func(dbTx *PebbleDBTransaction) error {
				_, err := dbTx.Has(database.MakeBucket(nil).Key([]byte("key")))
				return err
			},
			shouldReturnError: true,
		},
		{
			name: "Delete",
			function: func(dbTx *PebbleDBTransaction) error {
				return dbTx.Delete(database.MakeBucket(nil).Key([]byte("key")))
			},
			shouldReturnError: true,
		},
		{
			name: "Cursor",
			function: func(dbTx *PebbleDBTransaction) error {
				_, err := dbTx.Cursor(database.MakeBucket([]byte("bucket")))
				return err
			},
			shouldReturnError: true,
		},
		{
			name:              "Rollback",
			function:          (*PebbleDBTransaction).Rollback,
			shouldReturnError: true,
		},
		{
			name:              "Commit",
			function:          (*PebbleDBTransaction).Commit,
			shouldReturnError: true,
		},
		{
			name:              "RollbackUnlessClosed",
			function:          (*PebbleDBTransaction).RollbackUnlessClosed,
			shouldReturnError: false,
		},
	}

	for _, test := range tests {
		func() {
			ldb, teardownFunc := prepareDatabaseForTest(t, "TestTransactionCloseErrors")
			defer teardownFunc()

			// Begin a new transaction to test Commit
			commitTx, err := ldb.Begin()
			if err != nil {
				t.Fatalf("TestTransactionCloseErrors: Begin unexpectedly failed: %s", err)
			}
			defer func() {
				err := commitTx.RollbackUnlessClosed()
				if err != nil {
					t.Fatalf("TestTransactionCloseErrors: RollbackUnlessClosed unexpectedly failed: %s", err)
				}
			}()

			// Commit the Commit test transaction
			err = commitTx.Commit()
			if err != nil {
				t.Fatalf("TestTransactionCloseErrors: Commit unexpectedly failed: %s", err)
			}

			// Begin a new transaction to test Rollback
			rollbackTx, err := ldb.Begin()
			if err != nil {
				t.Fatalf("TestTransactionCloseErrors: Begin unexpectedly failed: %s", err)
			}
			defer func() {
				err := rollbackTx.RollbackUnlessClosed()
				if err != nil {
					t.Fatalf("TestTransactionCloseErrors: RollbackUnlessClosed unexpectedly failed: %s", err)
				}
			}()

			// Rollback the Rollback test transaction
			err = rollbackTx.Rollback()
			if err != nil {
				t.Fatalf("TestTransactionCloseErrors: Rollback unexpectedly failed: %s", err)
			}

			expectedErrContainsString := "closed transaction"

			// Make sure that the test function returns a "closed transaction" error
			// for both the commitTx and the rollbackTx
			for _, closedTx := range []database.Transaction{commitTx, rollbackTx} {
				err = test.function(closedTx.(*PebbleDBTransaction))
				if test.shouldReturnError {
					if err == nil {
						t.Fatalf("TestTransactionCloseErrors: %s unexpectedly succeeded", test.name)
					}
					if !strings.Contains(err.Error(), expectedErrContainsString) {
						t.Fatalf("TestTransactionCloseErrors: %s returned wrong error. Want: %s, got: %s",
							test.name, expectedErrContainsString, err)
					}
				} else {
					if err != nil {
						t.Fatalf("TestTransactionCloseErrors: %s unexpectedly failed: %s", test.name, err)
					}
				}
			}
		}()
	}
}

func TestTransactionHasBatchIsolation(t *testing.T) {
	ldb, teardownFunc := prepareDatabaseForTest(t, "TestTransactionHasBatchIsolation")
	defer teardownFunc()

	// Begin a new transaction
	tx, err := ldb.Begin()
	if err != nil {
		t.Fatalf("TestTransactionHasBatchIsolation: Begin failed: %s", err)
	}
	defer func() { _ = tx.RollbackUnlessClosed() }()

	// Put a UTXO-like key-value pair
	key := database.MakeBucket([]byte("utxo")).Key([]byte("txid:0"))
	value := []byte("1000:somscript")
	err = tx.Put(key, value)
	if err != nil {
		t.Fatalf("TestTransactionHasBatchIsolation: Put failed: %s", err)
	}

	// Check if key exists in transaction
	exists, err := tx.Has(key)
	if err != nil {
		t.Fatalf("TestTransactionHasBatchIsolation: Has failed: %s", err)
	}
	if !exists {
		t.Fatalf("TestTransactionHasBatchIsolation: Has returned false for key in batch")
	}

	// Verify key doesn't exist in database before commit
	exists, err = ldb.Has(key)
	if err != nil {
		t.Fatalf("TestTransactionHasBatchIsolation: DB Has failed: %s", err)
	}
	if exists {
		t.Fatalf("TestTransactionHasBatchIsolation: Key found in database before commit")
	}

	// Get the value from the transaction
	data, err := tx.Get(key)
	if err != nil {
		t.Fatalf("TestTransactionHasBatchIsolation: Get failed: %s", err)
	}
	if !reflect.DeepEqual(data, value) {
		t.Fatalf("TestTransactionHasBatchIsolation: Get returned wrong value. Want: %s, Got: %s", value, data)
	}

	// Delete the key in the transaction
	err = tx.Delete(key)
	if err != nil {
		t.Fatalf("TestTransactionHasBatchIsolation: Delete failed: %s", err)
	}

	// Check if key is marked as deleted in transaction
	exists, err = tx.Has(key)
	if err != nil {
		t.Fatalf("TestTransactionHasBatchIsolation: Has after Delete failed: %s", err)
	}
	if exists {
		t.Fatalf("TestTransactionHasBatchIsolation: Has returned true for deleted key")
	}

	// Commit the transaction
	err = tx.Commit()
	if err != nil {
		t.Fatalf("TestTransactionHasBatchIsolation: Commit failed: %s", err)
	}

	// Verify key doesn't exist in database after commit
	exists, err = ldb.Has(key)
	if err != nil {
		t.Fatalf("TestTransactionHasBatchIsolation: DB Has after commit failed: %s", err)
	}
	if exists {
		t.Fatalf("TestTransactionHasBatchIsolation: Key found in database after delete commit")
	}
}
