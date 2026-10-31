package app

import (
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/infrastructure/config"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database/pebble"
)

// writesAfterCloseDB counts writes made after Close instead of passing them to Pebble, which panics on them.
type writesAfterCloseDB struct {
	database.Database
	lock              sync.Mutex
	closed            bool
	writesAfterClosed int
}

func (db *writesAfterCloseDB) write(write func() error) error {
	db.lock.Lock()
	defer db.lock.Unlock()
	if db.closed {
		db.writesAfterClosed++
		return nil
	}
	return write()
}

func (db *writesAfterCloseDB) Put(key *database.Key, value []byte) error {
	return db.write(func() error { return db.Database.Put(key, value) })
}

func (db *writesAfterCloseDB) Delete(key *database.Key) error {
	return db.write(func() error { return db.Database.Delete(key) })
}

func (db *writesAfterCloseDB) Close() error {
	db.lock.Lock()
	defer db.lock.Unlock()
	db.closed = true
	return db.Database.Close()
}

// TestDNSSeedLookupReturningAfterStopDoesNotWriteToTheDatabase lets the connection manager's DNS seeding lookup return
// only after Stop has returned and the database has been closed, as app.main closes it. Nothing waits for the
// seeding goroutine, so the addresses it found must not be written to the closed database.
func TestDNSSeedLookupReturningAfterStopDoesNotWriteToTheDatabase(t *testing.T) {
	lookupStarted := make(chan struct{})
	releaseLookup := make(chan struct{})
	var startedOnce sync.Once

	cfg := config.DefaultConfig()
	cfg.AppDir = t.TempDir()
	cfg.DNSSeed = "seed.invalid"
	cfg.TargetOutboundPeers = 1
	cfg.Lookup = func(string) ([]net.IP, error) {
		startedOnce.Do(func() { close(lookupStarted) })
		<-releaseLookup
		return []net.IP{net.IPv4(44, 1, 2, 3)}, nil
	}
	pebbleDB, err := pebble.NewPebbleDB(t.TempDir(), 8)
	if err != nil {
		t.Fatalf("NewPebbleDB: %+v", err)
	}
	db := &writesAfterCloseDB{Database: pebbleDB}
	componentManager, err := NewComponentManager(cfg, db, make(chan struct{}))
	if err != nil {
		t.Fatalf("NewComponentManager: %+v", err)
	}
	// The net adapter is left unstarted: no listeners are needed. The connections loop has no addresses, so it seeds.
	componentManager.connectionManager.Start()
	select {
	case <-lookupStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("the connection manager did not start DNS seeding")
	}

	if !componentManager.Stop() {
		t.Fatal("Stop reported that the node did not shut down cleanly")
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %+v", err)
	}
	close(releaseLookup)
	deadline := time.Now().Add(10 * time.Second)
	for strings.Contains(goroutineStacks(), "dnsseed.SeedFromDNS") {
		if time.Now().After(deadline) {
			t.Fatal("the DNS seeding goroutine did not finish")
		}
		time.Sleep(time.Millisecond)
	}

	db.lock.Lock()
	defer db.lock.Unlock()
	if db.writesAfterClosed > 0 {
		t.Fatalf("addresses found by a DNS seeding lookup were written to the closed database (%d writes)", db.writesAfterClosed)
	}
}
