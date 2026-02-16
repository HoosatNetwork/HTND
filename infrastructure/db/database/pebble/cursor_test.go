package pebble

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/Hoosat-Oy/HTND/infrastructure/db/database"
)

func validateCurrentCursorKeyAndValue(t *testing.T, testName string, cursor database.Cursor,
	expectedKey *database.Key, expectedValue []byte) {

	cursorKey, err := cursor.Key()
	if err != nil {
		t.Fatalf("%s: Key unexpectedly failed: %s", testName, err)
	}
	if !reflect.DeepEqual(cursorKey, expectedKey) {
		t.Fatalf("%s: Key returned wrong key. Want: %s, got: %s",
			testName, string(expectedKey.Bytes()), string(cursorKey.Bytes()))
	}
	cursorValue, err := cursor.Value()
	if err != nil {
		t.Fatalf("%s: Value unexpectedly failed for key %s: %s",
			testName, cursorKey, err)
	}
	if !bytes.Equal(cursorValue, expectedValue) {
		t.Fatalf("%s: Value returned wrong value for key %s. Want: %s, got: %s",
			testName, cursorKey, string(expectedValue), string(cursorValue))
	}
}

func recoverFromClosedCursorPanic(t *testing.T, testName string) {
	panicErr := recover()
	if panicErr == nil {
		t.Fatalf("%s: cursor unexpectedly didn't panic after being closed", testName)
	}
	expectedPanicErr := "closed cursor"
	if !strings.Contains(fmt.Sprintf("%v", panicErr), expectedPanicErr) {
		t.Fatalf("%s: cursor panicked with wrong message. Want: %v, got: %s",
			testName, expectedPanicErr, panicErr)
	}
}

// TestCursorSanity validates typical cursor usage, including
// opening a cursor over some existing data, seeking back
// and forth over that data, and getting some keys/values out
// of the cursor.
func TestCursorSanity(t *testing.T) {
	ldb, teardownFunc := prepareDatabaseForTest(t, "TestCursorSanity")
	defer teardownFunc()

	// Write some data to the database
	bucket := database.MakeBucket([]byte("bucket"))
	for i := range 10 {
		key := fmt.Sprintf("key%d", i)
		value := fmt.Sprintf("value%d", i)
		err := ldb.Put(bucket.Key([]byte(key)), []byte(value))
		if err != nil {
			t.Fatalf("TestCursorSanity: Put unexpectedly failed: %s", err)
		}
	}

	// Open a new cursor
	cursor, err := ldb.Cursor(bucket)
	if err != nil {
		t.Fatalf("TestCursorSanity: ldb.Cursor unexpectedly failed: : %s", err)
	}
	defer func() {
		err := cursor.Close()
		if err != nil {
			t.Fatalf("TestCursorSanity: Close unexpectedly failed: %s", err)
		}
	}()

	// Seek to first key and make sure its key and value are correct
	hasNext := cursor.First()
	if !hasNext {
		t.Fatalf("TestCursorSanity: First unexpectedly returned non-existence")
	}
	expectedKey := bucket.Key([]byte("key0"))
	expectedValue := []byte("value0")
	validateCurrentCursorKeyAndValue(t, "TestCursorSanity", cursor, expectedKey, expectedValue)

	// Seek past the last key in the bucket
	err = cursor.Seek(bucket.Key([]byte("key:")))
	if err == nil {
		t.Fatalf("TestCursorSanity: Seek unexpectedly succeeded")
	}
	if !database.IsNotFoundError(err) {
		t.Fatalf("TestCursorSanity: Seek returned wrong error: %s", err)
	}

	// Seek to the last key
	err = cursor.Seek(bucket.Key([]byte("key9")))
	if err != nil {
		t.Fatalf("TestCursorSanity: Seek unexpectedly failed: %s", err)
	}
	expectedKey = bucket.Key([]byte("key9"))
	expectedValue = []byte("value9")
	validateCurrentCursorKeyAndValue(t, "TestCursorSanity", cursor, expectedKey, expectedValue)

	// Call Next to get to the end of the cursor. This should
	// return false to signify that there are no items after that.
	// Key and Value calls should return ErrNotFound.
	hasNext = cursor.Next()
	if hasNext {
		t.Fatalf("TestCursorSanity: Next after last value is unexpectedly not done")
	}
	_, err = cursor.Key()
	if err == nil {
		t.Fatalf("TestCursorSanity: Key unexpectedly succeeded")
	}
	if !database.IsNotFoundError(err) {
		t.Fatalf("TestCursorSanity: Key returned wrong error: %s", err)
	}
	_, err = cursor.Value()
	if err == nil {
		t.Fatalf("TestCursorSanity: Value unexpectedly succeeded")
	}
	if !database.IsNotFoundError(err) {
		t.Fatalf("TestCursorSanity: Value returned wrong error: %s", err)
	}
}

func TestCursorCloseErrors(t *testing.T) {
	tests := []struct {
		name     string
		function func(dbTx database.Cursor) error
	}{
		{
			name: "Seek",
			function: func(cursor database.Cursor) error {
				return cursor.Seek(database.MakeBucket(nil).Key([]byte{}))
			},
		},
		{
			name: "Key",
			function: func(cursor database.Cursor) error {
				_, err := cursor.Key()
				return err
			},
		},
		{
			name: "Value",
			function: func(cursor database.Cursor) error {
				_, err := cursor.Value()
				return err
			},
		},
		{
			name: "Close",
			function: func(cursor database.Cursor) error {
				return cursor.Close()
			},
		},
	}

	for _, test := range tests {
		func() {
			ldb, teardownFunc := prepareDatabaseForTest(t, "TestCursorCloseErrors")
			defer teardownFunc()

			// Open a new cursor
			cursor, err := ldb.Cursor(database.MakeBucket(nil))
			if err != nil {
				t.Fatalf("TestCursorCloseErrors: ldb.Cursor unexpectedly failed: %s", err)
			}

			// Close the cursor
			err = cursor.Close()
			if err != nil {
				t.Fatalf("TestCursorCloseErrors: Close unexpectedly failed: %s", err)
			}

			expectedErrContainsString := "closed cursor"

			// Make sure that the test function returns a "closed cursor" error
			err = test.function(cursor)
			if err == nil {
				t.Fatalf("TestCursorCloseErrors: %s unexpectedly succeeded", test.name)
			}
			if !strings.Contains(err.Error(), expectedErrContainsString) {
				t.Fatalf("TestCursorCloseErrors: %s returned wrong error. Want: %s, got: %s",
					test.name, expectedErrContainsString, err)
			}
		}()
	}
}

func TestCursorCloseFirstAndNext(t *testing.T) {
	ldb, teardownFunc := prepareDatabaseForTest(t, "TestCursorCloseFirstAndNext")
	defer teardownFunc()

	// Write some data to the database
	for i := range 10 {
		key := fmt.Sprintf("key%d", i)
		value := fmt.Sprintf("value%d", i)
		err := ldb.Put(database.MakeBucket([]byte("bucket")).Key([]byte(key)), []byte(value))
		if err != nil {
			t.Fatalf("TestCursorCloseFirstAndNext: Put unexpectedly failed: %s", err)
		}
	}

	// Open a new cursor
	cursor, err := ldb.Cursor(database.MakeBucket([]byte("bucket")))
	if err != nil {
		t.Fatalf("TestCursorCloseFirstAndNext: ldb.Cursor unexpectedly failed: %s", err)
	}

	// Close the cursor
	err = cursor.Close()
	if err != nil {
		t.Fatalf("TestCursorCloseFirstAndNext: Close unexpectedly failed: %s", err)
	}

	// We expect First to panic
	func() {
		defer recoverFromClosedCursorPanic(t, "TestCursorCloseFirstAndNext")
		cursor.First()
	}()

	// We expect Next to panic
	func() {
		defer recoverFromClosedCursorPanic(t, "TestCursorCloseFirstAndNext")
		cursor.Next()
	}()
}

func TestCursorUTXOIteration(t *testing.T) {
	ldb, teardownFunc := prepareDatabaseForTest(t, "TestCursorUTXOIteration")
	defer teardownFunc()

	bucket := database.MakeBucket([]byte("utxo"))
	for i := range 10 {
		key := bucket.Key(fmt.Appendf(nil, "txid:%d", i))
		value := fmt.Appendf(nil, "amount:%d", i)
		if err := ldb.Put(key, value); err != nil {
			t.Fatalf("TestCursorUTXOIteration: Put failed: %s", err)
		}
	}

	cursor, err := ldb.Cursor(bucket)
	if err != nil {
		t.Fatalf("TestCursorUTXOIteration: Cursor failed: %s", err)
	}
	defer cursor.Close()

	i := 0
	for ok := cursor.First(); ok; ok = cursor.Next() {
		if i >= 10 {
			t.Fatalf("TestCursorUTXOIteration: Cursor iterated beyond expected  Ascending: %d", i)
		}
		expectedKey := bucket.Key(fmt.Appendf(nil, "txid:%d", i))
		expectedValue := fmt.Appendf(nil, "amount:%d", i)
		validateCurrentCursorKeyAndValue(t, "TestCursorUTXOIteration", cursor, expectedKey, expectedValue)
		i++
	}

	if i != 10 {
		t.Fatalf("TestCursorUTXOIteration: Expected 10 iterations, got %d", i)
	}
}

func TestCursorUTXOEmptyAndNonSequential(t *testing.T) {
	ldb, teardownFunc := prepareDatabaseForTest(t, "TestCursorUTXOEmptyAndNonSequential")
	defer teardownFunc()

	// Test empty bucket
	bucket := database.MakeBucket([]byte("utxo"))
	cursor, err := ldb.Cursor(bucket)
	if err != nil {
		t.Fatalf("TestCursorUTXOEmptyAndNonSequential: Cursor failed: %s", err)
	}
	defer cursor.Close()
	if cursor.First() {
		t.Fatalf("TestCursorUTXOEmptyAndNonSequential: First returned true for empty bucket")
	}

	// Test non-sequential keys
	keys := []string{"txid:0", "txid:2", "txid:5"}
	for i, k := range keys {
		key := bucket.Key([]byte(k))
		value := fmt.Appendf(nil, "amount:%d", i)
		if err := ldb.Put(key, value); err != nil {
			t.Fatalf("TestCursorUTXOEmptyAndNonSequential: Put failed: %s", err)
		}
	}

	cursor, err = ldb.Cursor(bucket)
	if err != nil {
		t.Fatalf("TestCursorUTXOEmptyAndNonSequential: Cursor failed: %s", err)
	}
	defer cursor.Close()

	i := 0
	for ok := cursor.First(); ok; ok = cursor.Next() {
		if i >= len(keys) {
			t.Fatalf("TestCursorUTXOEmptyAndNonSequential: Cursor iterated beyond expected %d keys", len(keys))
		}
		expectedKey := bucket.Key([]byte(keys[i]))
		expectedValue := fmt.Appendf(nil, "amount:%d", i)
		validateCurrentCursorKeyAndValue(t, "TestCursorUTXOEmptyAndNonSequential", cursor, expectedKey, expectedValue)
		i++
	}
	if i != len(keys) {
		t.Fatalf("TestCursorUTXOEmptyAndNonSequential: Expected %d iterations, got %d", len(keys), i)
	}
}
