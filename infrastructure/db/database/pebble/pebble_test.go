package pebble

import (
	"os"
	"reflect"
	"testing"

	"github.com/Hoosat-Oy/HTND/infrastructure/db/database"
)

func prepareDatabaseForTest(t *testing.T, testName string) (ldb *DB, teardownFunc func()) {
	// Create a temp db to run tests against
	path, err := os.MkdirTemp("", testName)
	if err != nil {
		t.Fatalf("%s: TempDir unexpectedly failed: %s", testName, err)
	}
	ldb, err = NewPebbleDB(path, 8)
	if err != nil {
		t.Fatalf("%s: NewPebbleDB unexpectedly failed: %s", testName, err)
	}
	teardownFunc = func() {
		err = ldb.Close()
		if err != nil {
			t.Fatalf("%s: Close unexpectedly failed: %s", testName, err)
		}
	}
	return ldb, teardownFunc
}

func TestPebbleDBSanity(t *testing.T) {
	ldb, teardownFunc := prepareDatabaseForTest(t, "TestpebbleDBSanity")
	defer teardownFunc()

	// Put something into the db
	key := database.MakeBucket(nil).Key([]byte("key"))
	putData := []byte("Hello world!")
	err := ldb.Put(key, putData)
	if err != nil {
		t.Fatalf("TestpebbleDBSanity: Put returned unexpected error: %s", err)
	}

	// Get from the key previously put to
	getData, err := ldb.Get(key)
	if err != nil {
		t.Fatalf("TestpebbleDBSanity: Get returned unexpected error: %s", err)
	}

	// Make sure that the put data and the get data are equal
	if !reflect.DeepEqual(getData, putData) {
		t.Fatalf("TestpebbleDBSanity: get data and put data are not equal. Put: %s, got: %s",
			string(putData), string(getData))
	}
}

func TestPebbleDBTransactionSanity(t *testing.T) {
	ldb, teardownFunc := prepareDatabaseForTest(t, "TestpebbleDBTransactionSanity")
	defer teardownFunc()

	// Case 1. Write in tx and then read directly from the DB
	// Begin a new transaction
	tx, err := ldb.Begin()
	if err != nil {
		t.Fatalf("TestpebbleDBTransactionSanity: Begin unexpectedly failed: %s", err)
	}

	// Put something into the transaction
	key := database.MakeBucket(nil).Key([]byte("key"))
	putData := []byte("Hello world!")
	err = tx.Put(key, putData)
	if err != nil {
		t.Fatalf("TestpebbleDBTransactionSanity: Put returned unexpected error: %s", err)
	}

	// Get from the key previously put to. Since the tx is not yet committed, this should return ErrNotFound.
	_, err = ldb.Get(key)
	if err == nil {
		t.Fatalf("TestpebbleDBTransactionSanity: Get unexpectedly succeeded")
	}
	if !database.IsNotFoundError(err) {
		t.Fatalf("TestpebbleDBTransactionSanity: Get returned wrong error: %s", err)
	}

	// Commit the transaction
	err = tx.Commit()
	if err != nil {
		t.Fatalf("TestpebbleDBTransactionSanity: Commit returned unexpected error: %s", err)
	}

	// Get from the key previously put to. Now that the tx was committed, this should succeed.
	getData, err := ldb.Get(key)
	if err != nil {
		t.Fatalf("TestpebbleDBTransactionSanity: Get returned unexpected error: %s", err)
	}

	// Make sure that the put data and the get data are equal
	if !reflect.DeepEqual(getData, putData) {
		t.Fatalf("TestpebbleDBTransactionSanity: get data and put data are not equal. Put: %s, got: %s",
			string(putData), string(getData))
	}

	// Case 2. Write directly to the DB and then read from a tx
	// Put something into the db
	key = database.MakeBucket(nil).Key([]byte("key2"))
	putData = []byte("Goodbye world!")
	err = ldb.Put(key, putData)
	if err != nil {
		t.Fatalf("TestpebbleDBTransactionSanity: Put returned unexpected error: %s", err)
	}

	// Begin a new transaction
	tx, err = ldb.Begin()
	if err != nil {
		t.Fatalf("TestpebbleDBTransactionSanity: Begin unexpectedly failed: %s", err)
	}

	// Get from the key previously put to
	getData, err = tx.Get(key)
	if err != nil {
		t.Fatalf("TestpebbleDBTransactionSanity: Get returned unexpected error: %s", err)
	}

	// Make sure that the put data and the get data are equal
	if !reflect.DeepEqual(getData, putData) {
		t.Fatalf("Test MioTestpebbleDBTransactionSanity: get data and put data are not equal. Put: %s, got: %s",
			string(putData), string(getData))
	}

	// Rollback the transaction
	err = tx.Rollback()
	if err != nil {
		t.Fatalf("TestpebbleDBTransactionSanity: rollback returned unexpected error: %s", err)
	}
}
