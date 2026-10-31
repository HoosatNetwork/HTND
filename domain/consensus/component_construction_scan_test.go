package consensus_test

import (
	"errors"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/app"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/config"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database"
)

var errComponentConstruction = errors.New("injected address manager construction failure")

// Wait for the startup scan to hold a cursor before failing address-manager
// construction. This makes the failure independent of goroutine scheduling.
type constructionFailureDB struct {
	database.Database
	hold   *scanHold
	failed chan struct{}
}

func (db *constructionFailureDB) Cursor(bucket *database.Bucket) (database.Cursor, error) {
	if string(bucket.Path()) == "not-banned-addresses/" {
		select {
		case <-db.hold.reached:
		case <-time.After(scanTestTimeout):
			return nil, errors.New("startup scan never opened its cursor")
		}
		close(db.failed)
		return nil, errComponentConstruction
	}
	return db.Database.Cursor(bucket)
}

func TestComponentConstructionFailureWaitsForPruningPointScan(t *testing.T) {
	forScanDatabases(t, func(t *testing.T, cfg *consensus.Config, databaseInstance database.Database) {
		n, teardown := newPruningMoveNodeWithDatabase(t, cfg, "TestComponentConstructionFailureWaitsForPruningPointScan", true, databaseInstance)
		defer teardown()
		n.insertUntilThePruningPointMoves(n.insertAndUpdate, nil)
		n.waitUntilUpdated()
		n.node.Close()
		// Leave an update pending for component construction to resume at startup.
		n.insertUntilThePruningPointMoves(func(block *externalapi.DomainBlock) error {
			return n.node.ValidateAndInsertBlock(block, true, true)
		}, nil)
		hold := n.db.armHold()
		defer hold.Release()
		db := &constructionFailureDB{Database: n.db, hold: hold, failed: make(chan struct{})}
		appConfig := config.DefaultConfig()
		appConfig.AppDir = t.TempDir()
		appConfig.ActiveNetParams = &n.nodeConfig.Params
		result := make(chan error, 1)
		go func() {
			_, err := app.NewComponentManager(appConfig, db, make(chan struct{}))
			result <- err
		}()
		select {
		case <-db.failed:
		case err := <-result:
			t.Fatalf("construction returned before the injected failure: %v", err)
		case <-time.After(scanTestTimeout):
			t.Fatal("construction did not reach the injected failure")
		}
		var constructionErr error
		returned := false
		select {
		case constructionErr = <-result:
			returned = true
			t.Error("construction returned while its scanner held an open cursor")
		case <-time.After(100 * time.Millisecond):
		}
		hold.Release()
		if !returned {
			select {
			case constructionErr = <-result:
			case <-time.After(scanTestTimeout):
				t.Fatal("construction did not join its scanner")
			}
		}
		if !errors.Is(constructionErr, errComponentConstruction) {
			t.Errorf("construction error: %v, want the injected error", constructionErr)
		}
		select {
		case <-hold.closed:
		default:
			t.Error("construction returned before closing the scan cursor")
		}
		// The failure notification and the wait above do not order cancellation before Release. A check that
		// finishes first may commit; construction must join it either way. Cancellation while held is covered by
		// TestStopDuringThePruningPointUTXOSetCheck.
		// Also wait on the unfixed implementation, so teardown does not close
		// the database beneath the cursor after this regression fails.
		select {
		case <-hold.closed:
		case <-time.After(scanTestTimeout):
			t.Fatal("scan cursor was never closed")
		}
		if returned {
			// The unfixed worker also finalizes after closing its cursor. Let it
			// finish before teardown closes the DB, preserving the actual failure.
			n.waitUntilUpdated()
		}
	})
}
