package consensus_test

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/database/serialization"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/processes/pruningmanager"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/testutils"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database/ldb"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database/pebble"
)

var errScanRead = errors.New("injected pruning point UTXO set read error")

// scanReadErrorDB faults one scan, either after the first entry or immediately
// before stopKey. Like the native iterators, it reports a failed Next via Close.
// LevelDB's native error propagation is tested separately in package ldb.
type scanReadErrorDB struct {
	database.Database
	armed   bool
	stopKey []byte
	cursor  *scanReadErrorCursor
}

func (db *scanReadErrorDB) Cursor(bucket *database.Bucket) (database.Cursor, error) {
	cursor, err := db.Database.Cursor(bucket)
	if err != nil || !db.armed || !bytes.HasSuffix(bucket.Path(), []byte("/pruning-point-utxo-set/")) {
		return cursor, err
	}
	db.armed = false
	db.cursor = &scanReadErrorCursor{Cursor: cursor, stopKey: db.stopKey}
	return db.cursor, nil
}

type scanReadErrorCursor struct {
	database.Cursor
	stopKey []byte
	failed  bool
	closed  bool
}

func (c *scanReadErrorCursor) Next() bool {
	if c.stopKey == nil {
		c.failed = true
		return false
	}
	if !c.Cursor.Next() {
		return false
	}
	key, err := c.Cursor.Key()
	if err == nil && bytes.Equal(key.Bytes(), c.stopKey) {
		c.failed = true
		return false
	}
	return true
}

func (c *scanReadErrorCursor) Close() error {
	c.closed = true
	if err := c.Cursor.Close(); err != nil {
		return err
	}
	if c.failed {
		return errScanRead
	}
	return nil
}

func forScanDatabases(t *testing.T, test func(*testing.T, *consensus.Config, database.Database)) {
	for _, backend := range []string{"leveldb", "pebble"} {
		t.Run(backend, func(t *testing.T) {
			testutils.ForAllNets(t, true, func(t *testing.T, cfg *consensus.Config) {
				var db database.Database
				var err error
				if backend == "leveldb" {
					db, err = ldb.NewLevelDB(t.TempDir(), 8)
				} else {
					db, err = pebble.OpenPebbleDB(t.TempDir(), 8)
				}
				if err != nil {
					t.Fatal(err)
				}
				test(t, cfg, db)
			})
		})
	}
}

func TestPruningPointUTXOSetReadErrorLeavesUpdatePending(t *testing.T) {
	forScanDatabases(t, func(t *testing.T, cfg *consensus.Config, db database.Database) {
		testPruningPointUTXOSetReadError(t, cfg, db, false)
	})
}

func TestPruningPointUTXOSetReadErrorCannotApproveCorruptTail(t *testing.T) {
	forScanDatabases(t, func(t *testing.T, cfg *consensus.Config, db database.Database) {
		testPruningPointUTXOSetReadError(t, cfg, db, true)
	})
}

func testPruningPointUTXOSetReadError(t *testing.T, cfg *consensus.Config, db database.Database, corruptTail bool) {
	n, teardown := newPruningMoveNodeWithDatabase(t, cfg, "TestPruningPointUTXOSetReadError", true, db)
	defer teardown()
	firstPruningPoint := n.insertUntilThePruningPointMoves(n.insertAndUpdate, nil)
	n.waitUntilUpdated()
	// Run the next scan synchronously to assert the exact error without waiting
	// for logging or racing the background worker's mismatch callback.
	n.node.Close()
	fault := &scanReadErrorDB{Database: db}
	n.db.Database = fault
	if corruptTail {
		bucket := database.MakeBucket(n.dbPrefix.Serialize()).Bucket([]byte("pruning-point-utxo-set"))
		cursor, err := db.Cursor(bucket)
		if err != nil {
			t.Fatal(err)
		}
		if !cursor.First() {
			cursor.Close()
			t.Fatal("setup: empty pruning point UTXO set")
		}
		value, err := cursor.Value()
		value = bytes.Clone(value)
		closeErr := cursor.Close()
		if err != nil || closeErr != nil {
			t.Fatalf("reading entry: %v, closing cursor: %v", err, closeErr)
		}
		var id [externalapi.DomainHashSize]byte
		for i := range id {
			id[i] = 0xff
		}
		outpoint := externalapi.NewDomainOutpoint(externalapi.NewDomainTransactionIDFromByteArray(&id), 0xfffff)
		suffix, err := serialization.DomainOutpointToDbOutpoint(outpoint).MarshalVT()
		if err != nil {
			t.Fatal(err)
		}
		key := bucket.Key(suffix)
		if err := db.Put(key, value); err != nil {
			t.Fatal(err)
		}
		fault.stopKey = key.Bytes()
	}
	pruningPoint := n.insertUntilThePruningPointMoves(n.insertAndUpdate, nil)
	fault.armed = true
	err := consensus.ScanPruningPointUTXOSetForTest(n.node)
	if !errors.Is(err, errScanRead) || errors.Is(err, pruningmanager.ErrPruningPointUTXOSetMismatch) {
		t.Errorf("scan returned %v, want an ordinary read error", err)
	}
	if fault.cursor == nil || !fault.cursor.failed || !fault.cursor.closed {
		t.Fatal("scan did not reach the injected failure and close its cursor")
	}
	if !n.updating() {
		t.Error("read error finished the pending update")
	}
	n.requireRefused(pruningPoint, firstPruningPoint)
	if n.blockDeleted(firstPruningPoint) {
		t.Error("read error allowed deletion of the previous pruning point's blocks")
	}
	n.requireNoMismatch()
	// A complete retry proves that the unchanged set is healthy in the first
	// case and that the skipped trailing entry really corrupts it in the second.
	err = consensus.ScanPruningPointUTXOSetForTest(n.node)
	if corruptTail {
		if !errors.Is(err, pruningmanager.ErrPruningPointUTXOSetMismatch) || !n.updating() {
			t.Fatalf("complete retry must reject the corrupt set and leave it pending: %v", err)
		}
	} else {
		if err != nil || n.updating() {
			t.Fatalf("complete retry must finish the healthy set: %v", err)
		}
		n.requireServedAndVerified(pruningPoint)
	}
}

// Replacing a set at the same pruning point must invalidate the old snapshot by
// generation alone. A pruning-point comparison cannot detect this replacement.
func TestPruningPointUTXOSetImportSupersedesSamePointScan(t *testing.T) {
	forScanDatabases(t, func(t *testing.T, cfg *consensus.Config, db database.Database) {
		n, teardown := newPruningMoveNodeWithDatabase(t, cfg, "TestPruningPointUTXOSetImportSupersedesSamePointScan", true, db)
		defer teardown()
		firstPruningPoint := n.insertUntilThePruningPointMoves(n.insertAndUpdate, nil)
		n.waitUntilUpdated()
		n.node.Close()
		n.corruptPruningPointUTXOSet()
		pruningPoint := n.insertUntilThePruningPointMoves(n.insertAndUpdate, nil)
		hold := n.db.armHold()
		defer hold.Release()
		result := make(chan error, 1)
		go func() { result <- consensus.ScanPruningPointUTXOSetForTest(n.node) }()
		n.waitForHold(hold)
		pairs, err := n.source.GetPruningPointUTXOs(pruningPoint, nil, 10000)
		if err != nil || len(pairs) == 0 {
			t.Fatalf("source UTXOs: %d entries, %v", len(pairs), err)
		}
		if err := n.node.ClearImportedPruningPointData(); err != nil {
			t.Fatal(err)
		}
		if err := n.node.AppendImportedPruningPointUTXOs(pairs); err != nil {
			t.Fatal(err)
		}
		if err := n.node.ValidateAndInsertImportedPruningPoint(pruningPoint); err != nil {
			t.Fatal(err)
		}
		hold.Release()
		select {
		case err := <-result:
			if err != nil {
				t.Fatalf("superseded snapshot returned %v", err)
			}
		case <-time.After(scanTestTimeout):
			t.Fatal("superseded scan did not return")
		}
		if !n.updating() || n.blockDeleted(firstPruningPoint) {
			t.Fatal("superseded scan finished the update")
		}
		if err := consensus.ScanPruningPointUTXOSetForTest(n.node); err != nil {
			t.Fatal(err)
		}
		if n.updating() {
			t.Fatal("replacement set was not verified")
		}
		n.requireServedAndVerified(pruningPoint)
		n.requireNoMismatch()
	})
}
