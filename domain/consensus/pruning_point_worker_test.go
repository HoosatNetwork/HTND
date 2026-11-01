package consensus_test

import (
	"bytes"
	"sync"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/testutils"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database/pebble"
)

// The DB wrapper stalls
// precisely a destination write, including writes through a private transaction.
type pp42WriteDB struct {
	*pebble.DB
	mu   sync.Mutex
	hold *scanHold
}

func (d *pp42WriteDB) arm() *scanHold {
	d.mu.Lock()
	defer d.mu.Unlock()
	h := &scanHold{reached: make(chan struct{}), release: make(chan struct{}), closed: make(chan struct{})}
	d.hold = h
	return h
}
func (d *pp42WriteDB) pause(k *database.Key) {
	if !bytes.HasSuffix(k.Bucket().Path(), []byte("/pruning-point-utxo-set/")) {
		return
	}
	d.mu.Lock()
	h := d.hold
	d.hold = nil
	d.mu.Unlock()
	if h != nil {
		close(h.reached)
		<-h.release
	}
}
func (d *pp42WriteDB) Put(k *database.Key, v []byte) error { d.pause(k); return d.DB.Put(k, v) }
func (d *pp42WriteDB) Delete(k *database.Key) error        { d.pause(k); return d.DB.Delete(k) }
func (d *pp42WriteDB) BeginUnindexed() (database.Transaction, error) {
	tx, e := d.DB.BeginUnindexed()
	if e != nil {
		return nil, e
	}
	return &pp42WriteTx{tx, d}, nil
}

type pp42WriteTx struct {
	database.Transaction
	db *pp42WriteDB
}

func (tx *pp42WriteTx) Put(k *database.Key, v []byte) error {
	tx.db.pause(k)
	return tx.Transaction.Put(k, v)
}
func (tx *pp42WriteTx) Delete(k *database.Key) error { tx.db.pause(k); return tx.Transaction.Delete(k) }

func TestPP42DestinationWriteAllowsForeground(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, cfg *consensus.Config) {
		defer constants.ForceSetBlockVersion(1)
		cfg.DisableDifficultyAdjustment = true
		t.Setenv("HTND_TEST_MODE", "0")
		t.Setenv("HTND_PEBBLE_DISABLE_WAL", "0")
		raw, e := pebble.OpenPebbleDB(t.TempDir(), 8)
		if e != nil {
			t.Fatal(e)
		}
		db := &pp42WriteDB{DB: raw}
		// Build the reference and target on the synchronous path, then enable the target worker.
		n, closeNode := newPruningMoveNodeWithDatabase(t, cfg, "PP42Foreground", true, db)
		defer closeNode()
		n.node.Close()

		n.node, _, e = consensus.NewFactory().NewConsensus(n.nodeConfig, db, n.dbPrefix, nil)
		if e != nil {
			t.Fatal(e)
		}
		first := n.insertUntilThePruningPointMoves(n.insertAndUpdate, nil)
		n.waitUntilUpdated()
		n.requireServedAndVerified(first)
		h := db.arm()
		defer h.Release()
		// Insert until selection advances without invoking the updater under the test goroutine.
		target := n.insertUntilThePruningPointMoves(func(b *externalapi.DomainBlock) error { return n.node.ValidateAndInsertBlock(b, true, true) }, nil)
		update := make(chan error, 1)
		go func() { update <- n.node.UpdatePruningPointIfRequired() }()
		select {
		case <-h.reached:
		case <-time.After(10 * time.Second):
			t.Fatal("destination write not reached")
		}
		// A held destination operation must not own consensus, including at later selections.
		blocks := make([]*externalapi.DomainBlock, 90)
		for i := range blocks {
			blocks[i] = n.nextBlock()
		}
		progress := make(chan error, 1)
		go func() {
			for _, b := range blocks {
				if e := n.node.ValidateAndInsertBlock(b, true, true); e != nil {
					progress <- e
					return
				}
			}
			_, e := n.node.BuildBlockTemplate(&externalapi.DomainCoinbaseData{ScriptPublicKey: &externalapi.ScriptPublicKey{Script: []byte{0x51}}}, nil)
			progress <- e
		}()
		select {
		case e := <-progress:
			if e != nil {
				t.Fatal(e)
			}
		case <-time.After(10 * time.Second):
			h.Release()
			select {
			case <-progress:
			case <-time.After(10 * time.Second):
				t.Fatal("foreground still blocked after release")
			}
			<-update
			t.Fatal("foreground blocks/templates blocked by PP destination write")
		}
		n.requireRefused(target, n.pruningPoint())
		health, e := n.node.CheckUTXOHealth(n.pruningPoint())
		if e != nil || health.Ready {
			t.Fatalf("pending health = %+v, %v", health, e)
		}
		h.Release()
		if e = <-update; e != nil {
			t.Fatal(e)
		}
		if e = n.node.UpdatePruningPointIfRequired(); e != nil {
			t.Fatal(e)
		}
		n.waitUntilUpdated()
		n.requireNoMismatch()
		n.requireServedAndVerified(n.pruningPoint())
		// Exact serialized equality to the old path on the same block sequence.
		wantPoint, e := n.source.PruningPoint()
		if e != nil {
			t.Fatal(e)
		}
		if !wantPoint.Equal(n.pruningPoint()) {
			t.Fatal("selected points differ")
		}
		want, e := n.source.GetPruningPointUTXOs(wantPoint, nil, 10000)
		if e != nil {
			t.Fatal(e)
		}
		got, e := n.node.GetPruningPointUTXOs(wantPoint, nil, 10000)
		if e != nil {
			t.Fatal(e)
		}
		if len(got) != len(want) {
			t.Fatalf("set sizes %d != %d", len(got), len(want))
		}
		for i := range want {
			if *got[i].Outpoint != *want[i].Outpoint || !got[i].UTXOEntry.Equal(want[i].UTXOEntry) {
				t.Fatalf("different entry %d", i)
			}
		}
		n.node.Close()
		n.node, _, e = consensus.NewFactory().NewConsensus(n.nodeConfig, db, n.dbPrefix, nil)
		if e != nil {
			t.Fatal(e)
		}
		n.requireServedAndVerified(wantPoint)
	})
}

func (tx *pp42WriteTx) CommitSync() error {
	return tx.Transaction.(interface{ CommitSync() error }).CommitSync()
}
