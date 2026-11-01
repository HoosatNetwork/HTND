//go:build pruningtest

package consensus_test

import (
	"bytes"
	"math/big"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/domain"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/database/serialization"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/v2/domain/miningmanager/mempool"
	"github.com/HoosatNetwork/HTND/v2/domain/prefixmanager/prefix"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database/pebble"
)

// TestC4BlockCountReconciliation (PREP B7) requires Count() to equal the stored bodies
// after a normal restart that follows history deletion batches with no block commit after
// them, and after a start over count keys that contradict each other, which recounts the
// stored blocks once. The crash and the old-binary cases are in TestC4CrashMatrix and
// TestC4HistoryDowngrade.
func TestC4BlockCountReconciliation(t *testing.T) {
	defer constants.ForceSetBlockVersion(1)
	n, db, closeNode := newPP42PhaseNode(t, pp42CrashConfig(), nil)
	defer closeNode()
	reopen := func() {
		n.node.Close()
		var e error
		if n.node, _, e = consensus.NewFactory().NewConsensus(n.nodeConfig, db, n.dbPrefix, nil); e != nil {
			t.Fatal(e)
		}
		pp42RequireCountExact(t, n.node, db, n.dbPrefix)
		pp42RequireLegacyCountExact(t, db, n.dbPrefix)
	}
	for i := 0; i < 2; i++ {
		n.insertUntilThePruningPointMoves(n.insertAndUpdate, nil)
		n.waitUntilUpdated()
	}
	// No block after the move: its job's batches are the last commits.
	legacy, counted, deleted, bodies := pp42Counts(t, db, n.dbPrefix)
	if deleted <= counted || legacy <= bodies {
		t.Fatalf("no deletion after the last block commit: blocks-count %d, blocks-count-deleted %d, "+
			"blocks-deleted %d, stored bodies %d", legacy, counted, deleted, bodies)
	}
	t.Logf("normal restart: blocks-count %d behind %d deletions, stored bodies %d", legacy, deleted-counted, bodies)
	reopen()

	put := func(name string, count uint64) {
		value, e := (&serialization.DbBlockCount{Count: count}).MarshalVT()
		if e == nil {
			e = db.Put(database.MakeBucket(n.dbPrefix.Serialize()).Key([]byte(name)), value)
		}
		if e != nil {
			t.Fatal(e)
		}
	}
	// blocks-count-deleted above blocks-deleted.
	n.node.Close()
	_, _, deleted, _ = pp42Counts(t, db, n.dbPrefix)
	put("blocks-count-deleted", deleted+1)
	reopen()
	// More deletions after the last block commit than blocks-count holds.
	n.node.Close()
	put("blocks-count", 0)
	put("blocks-count-deleted", 0)
	reopen()
	n.insertUntilThePruningPointMoves(n.insertAndUpdate, nil)
	n.waitUntilUpdated()
	pp42RequireCountExact(t, n.node, db, n.dbPrefix)
}

// TestC4StopDuringHistoryPlan (PREP B6) stops a node while its history deletion job plans:
// every read of the snapshot the planner opens is delayed by pp42SlowRead, so the plan takes
// seconds. Close must return within pp42StopBound, before the plan finishes; the job keeps
// its record, and after a restart it finishes to the old path's exact history state, set
// and block count.
func TestC4StopDuringHistoryPlan(t *testing.T) {
	const pp42StopBound = time.Second
	defer constants.ForceSetBlockVersion(1)
	dir, want := pp42CrashAt(t, "history-recorded")
	raw, e := pebble.OpenPebbleDB(dir, 8)
	if e != nil {
		t.Fatal(e)
	}
	db := &pp42ViewDB{DB: raw}
	var planned atomic.Bool
	cfg := pp42CrashConfig()
	cfg.PruningPointJournalTestHook = func(p string) {
		switch p {
		case "history-recorded":
			// The resumed job is handed to the owner: every snapshot from here on is slow.
			db.mode.Store(4)
		case "history-planned":
			planned.Store(true)
		}
	}
	node, _, e := consensus.NewFactory().NewConsensus(cfg, db, &prefix.Prefix{}, nil)
	if e != nil {
		raw.Close()
		t.Fatal(e)
	}
	waitUntil(t, "the job is planning", func() bool { return db.slowReads.Load() >= 20 })
	start := time.Now()
	node.Close()
	stop, reads := time.Since(start), db.slowReads.Load()
	t.Logf("Close returned after %s, %d slow snapshot reads", stop, reads)
	if planned.Load() {
		t.Fatalf("the plan finished before the stop (%d reads): the fixture is too small", reads)
	}
	if stop > pp42StopBound {
		t.Fatalf("Close took %s while the job planned, bound %s", stop, pp42StopBound)
	}
	pp42RequireNoWorkerGoroutine(t)
	if !historyDeletionPending(t, raw, &prefix.Prefix{}) {
		t.Fatal("the stopped job lost its record")
	}
	if e = raw.Close(); e != nil {
		t.Fatal(e)
	}
	raw, node = pp42OpenRecovered(t, dir, want)
	pp42RequireHistory(t, node, dir)
	pp42RequireCountExact(t, node, raw, &prefix.Prefix{})
	headerOnly, withBody := pp42PastBodies(t, node)
	if headerOnly == 0 || withBody != 0 {
		t.Fatalf("below the pruning point: %d header-only blocks, %d with a body", headerOnly, withBody)
	}
	node.Close()
	if e = raw.Close(); e != nil {
		t.Fatal(e)
	}
}

// pp42Hold parks the owner that reaches hook name once it is armed, until released.
type pp42Hold struct {
	name             string
	armed            atomic.Bool
	reached, release chan struct{}
}

func newPP42Hold(name string) *pp42Hold {
	return &pp42Hold{name: name, reached: make(chan struct{}), release: make(chan struct{})}
}

func (h *pp42Hold) await(t *testing.T) {
	t.Helper()
	select {
	case <-h.reached:
	case <-time.After(60 * time.Second):
		t.Fatalf("no owner reached %s", h.name)
	}
}

// TestC4StagingConsensusWithOwnersBusy is UNINTERRUPTED-PP-MOVE D11 (PREP B8). A consensus
// whose owners are busy is closed by the domain, which then deletes its prefix. commit:
// committing a staging consensus closes the active one, whose history deletion owner is
// parked inside a batch before its commit and whose journal owner is parked inside the next
// move's derivation; one owner is released first (each order once), so the call must still
// wait for the other. discard: discarding a staging consensus closes it with its history
// deletion owner parked the same way. A staging consensus has no journal before its
// pruning point import (ResetJournalAfterImport), so its UTXO update runs in the foreground
// and there is no journal job to park. Required: the domain call does not return, and the
// prefix is not deleted, while an owner is held (joined before the prefix delete); after it
// returns the prefix is empty and stays empty, and no owner runs again (no write after
// Close).
func TestC4StagingConsensusWithOwnersBusy(t *testing.T) {
	defer constants.ForceSetBlockVersion(1)
	for _, name := range []string{"commit/journal-released-first", "commit/history-released-first", "discard"} {
		commit := name != "discard"
		t.Run(name, func(t *testing.T) {
			t.Setenv("HTND_TEST_MODE", "0")
			t.Setenv("HTND_PEBBLE_DISABLE_WAL", "0")

			raw, e := pebble.OpenPebbleDB(t.TempDir(), 8)
			if e != nil {
				t.Fatal(e)
			}
			defer raw.Close()
			db := &pp42ViewDB{DB: raw}
			history, journal := newPP42Hold("history-before-batch-commit"), newPP42Hold("derive-acceptance")
			var hookCalls atomic.Int64
			cfg := pp42CrashConfig()
			cfg.PruningPointJournalTestHook = func(p string) {
				hookCalls.Add(1)
				for _, h := range []*pp42Hold{history, journal} {
					if p == h.name && h.armed.CompareAndSwap(true, false) {
						close(h.reached)
						<-h.release
					}
				}
			}
			d, e := domain.New(cfg, mempool.DefaultConfig(&cfg.Params), db)
			if e != nil {
				t.Fatal(e)
			}
			busy, busyPrefix := d.Consensus(), &prefix.Prefix{}
			if !commit {
				if e = d.InitStagingConsensusWithoutGenesis(); e != nil {
					t.Fatal(e)
				}
				busy, busyPrefix = d.StagingConsensus(), (&prefix.Prefix{}).Flip()
				genesis := &externalapi.BlockWithTrustedData{Block: cfg.GenesisBlock,
					GHOSTDAGData: []*externalapi.BlockGHOSTDAGDataHashPair{{Hash: cfg.GenesisHash,
						GHOSTDAGData: externalapi.NewBlockGHOSTDAGData(0, big.NewInt(0), model.VirtualGenesisBlockHash,
							nil, nil, make(map[externalapi.DomainHash]externalapi.KType), externalapi.KType(1))}}}
				if e = busy.ValidateAndInsertBlockWithTrustedData(genesis, true); e != nil {
					t.Fatal(e)
				}
			}
			// Both owners busy at once: the history job of one move parked before a batch
			// commit, then the journal job of the next move parked in its derivation.
			pp42MineOneMove(t, busy)
			waitUntil(t, "the first move drained", func() bool {
				found, e := db.Has(database.MakeBucket(busyPrefix.Serialize()).Key([]byte(updatingPruningPointUTXOSetKeyName)))
				if e != nil {
					t.Fatal(e)
				}
				return !found && !historyDeletionPending(t, db, busyPrefix)
			})
			history.armed.Store(true)
			pp42MineOneMove(t, busy)
			history.await(t)
			holds := []*pp42Hold{history}
			if commit {
				journal.armed.Store(true)
				pp42MineOneMove(t, busy)
				journal.await(t)
				holds = []*pp42Hold{journal, history}
				if name == "commit/history-released-first" {
					holds = []*pp42Hold{history, journal}
				}
			}
			if commit {
				if e = d.InitStagingConsensusWithoutGenesis(); e != nil {
					t.Fatal(e)
				}
			}
			done := make(chan error, 1)
			go func() {
				if commit {
					done <- d.CommitStagingConsensus()
				} else {
					done <- d.DeleteStagingConsensus()
				}
			}()
			requireHeld := func(what string) {
				t.Helper()
				select {
				case e := <-done:
					t.Fatalf("the domain call returned (%v) while an owner was held at %s", e, what)
				case <-time.After(500 * time.Millisecond):
				}
				if pp42PrefixKeys(t, db, busyPrefix) == 0 {
					t.Fatalf("the prefix was deleted while an owner was held at %s", what)
				}
			}
			for _, h := range holds {
				requireHeld(h.name)
				close(h.release)
			}
			select {
			case e = <-done:
				if e != nil {
					t.Fatal(e)
				}
			case <-time.After(60 * time.Second):
				t.Fatal("the domain call did not return after both owners were released")
			}
			calls := hookCalls.Load()
			if keys := pp42PrefixKeys(t, db, busyPrefix); keys != 0 {
				t.Fatalf("%d keys left under the closed consensus's prefix", keys)
			}
			time.Sleep(time.Second)
			if keys := pp42PrefixKeys(t, db, busyPrefix); keys != 0 || hookCalls.Load() != calls {
				t.Fatalf("after the domain call returned: %d keys written under the closed prefix, %d owner hook calls",
					keys, hookCalls.Load()-calls)
			}
			d.Consensus().Close()
			if staging := d.StagingConsensus(); staging != nil {
				staging.Close()
			}
			pp42RequireNoWorkerGoroutine(t)
			buf := make([]byte, 8<<20)
			if stacks := buf[:runtime.Stack(buf, true)]; bytes.Contains(stacks, []byte("startHistoryDeletionOwner")) {
				t.Fatalf("a history deletion owner outlived Close:\n%s", stacks)
			}
		})
	}
}

// pp42MineOneMove mines on node's own templates until its pruning point moves, without
// waiting for the move's owners.
func pp42MineOneMove(t *testing.T, node externalapi.Consensus) {
	t.Helper()
	start, e := node.PruningPoint()
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; ; i++ {
		if i == 300 {
			t.Fatal("pruning point did not move")
		}
		template, e := node.BuildBlockTemplate(&externalapi.DomainCoinbaseData{
			ScriptPublicKey: &externalapi.ScriptPublicKey{Script: []byte{0x51}}}, nil)
		if e != nil {
			t.Fatal(e)
		}
		if e = node.ValidateAndInsertBlock(template.Block, true, true); e != nil {
			t.Fatal(e)
		}
		if e = node.UpdatePruningPointIfRequired(); e != nil {
			t.Fatal(e)
		}
		if point, e := node.PruningPoint(); e != nil || !point.Equal(start) {
			return
		}
	}
}

// pp42PrefixKeys counts the keys stored under a consensus prefix.
func pp42PrefixKeys(t *testing.T, db database.DataAccessor, p *prefix.Prefix) int {
	t.Helper()
	c, e := db.Cursor(database.MakeBucket(p.Serialize()))
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	keys := 0
	for ok := c.First(); ok; ok = c.Next() {
		keys++
	}
	return keys
}
