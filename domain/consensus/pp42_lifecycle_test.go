//go:build pruningtest

package consensus_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/datastructures/pruningstore"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/testutils"
	"github.com/HoosatNetwork/HTND/v2/domain/prefixmanager/prefix"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database/pebble"
)

// Faults apply only to the isolated read view, never foreground validation. The old
// healthy writer still provides the independent final-set/commitment reference.
type pp42ViewDB struct {
	*pebble.DB
	mode   atomic.Int32
	closed atomic.Bool
	// open counts snapshots handed out and not yet successfully closed.
	open atomic.Int64
	// avail, when non-zero, replaces the filesystem's free-space answer; queries
	// counts how often the journal asked.
	avail, queries atomic.Uint64
	// utxoWrite, when set, runs after each direct write to the pruning point UTXO set:
	// the legacy writer's, which the D1 disk fallback uses (fallback crash points).
	utxoWrite atomic.Pointer[func()]
	// failHistoryCommits fails that many commits of history deletion batches (a
	// transaction that puts block statuses and nothing but the block counts) with an
	// injected disk-full error; failed counts them.
	failHistoryCommits, failed atomic.Int32
	// slowReads counts the reads of snapshots opened in mode 4, each delayed by
	// pp42SlowRead (PREP B6: a large plan, made slow).
	slowReads atomic.Int64
}

const pp42SlowRead = 20 * time.Millisecond

var errPP42DiskFull = errors.New("injected: no space left on device")

func (d *pp42ViewDB) BeginUnindexed() (database.Transaction, error) {
	tx, e := d.DB.BeginUnindexed()
	if e != nil {
		return nil, e
	}
	return &pp42FaultTx{Transaction: tx, owner: d}, nil
}

type pp42FaultTx struct {
	database.Transaction
	owner                 *pp42ViewDB
	statusPuts, otherPuts int
}

func (tx *pp42FaultTx) Put(k *database.Key, v []byte) error {
	switch path := k.Bytes(); {
	case bytes.Contains(path, []byte("/block-statuses/")):
		tx.statusPuts++
	case !bytes.HasSuffix(path, []byte("blocks-count")) && !bytes.HasSuffix(path, []byte("blocks-deleted")):
		tx.otherPuts++
	}
	return tx.Transaction.Put(k, v)
}

func (tx *pp42FaultTx) BatchPut(pairs map[*database.Key][]byte) error {
	tx.otherPuts++
	return tx.Transaction.BatchPut(pairs)
}

func (tx *pp42FaultTx) Commit() error {
	if tx.statusPuts > 0 && tx.otherPuts == 0 && tx.owner.failHistoryCommits.Add(-1) >= 0 {
		tx.owner.failed.Add(1)
		_ = tx.Transaction.Rollback()
		return errPP42DiskFull
	}
	return tx.Transaction.Commit()
}

func (tx *pp42FaultTx) CommitSync() error {
	return tx.Transaction.(interface{ CommitSync() error }).CommitSync()
}

func (d *pp42ViewDB) Put(k *database.Key, v []byte) error {
	e := d.DB.Put(k, v)
	d.afterUTXOWrite(k, e)
	return e
}

func (d *pp42ViewDB) Delete(k *database.Key) error {
	e := d.DB.Delete(k)
	d.afterUTXOWrite(k, e)
	return e
}

func (d *pp42ViewDB) afterUTXOWrite(k *database.Key, e error) {
	if f := d.utxoWrite.Load(); f != nil && e == nil && bytes.Contains(k.Bytes(), []byte("/pruning-point-utxo-set/")) {
		(*f)()
	}
}

func (d *pp42ViewDB) AvailableDiskBytes() (uint64, error) {
	d.queries.Add(1)
	if v := d.avail.Load(); v != 0 {
		return v, nil
	}
	return d.DB.AvailableDiskBytes()
}

var errPP42ViewFault = errors.New("injected snapshot read fault")

func (d *pp42ViewDB) Close() error {
	if d.closed.CompareAndSwap(false, true) {
		return d.DB.Close()
	}
	return nil
}

func (d *pp42ViewDB) NewSnapshot() (database.Snapshot, error) {
	s, e := d.DB.NewSnapshot()
	if e != nil {
		return nil, e
	}
	d.open.Add(1)
	return &pp42View{s: s, mode: d.mode.Load(), owner: d}, nil
}

type pp42View struct {
	s      database.Snapshot
	mode   int32
	failed bool
	owner  *pp42ViewDB
	reads  int
}

func (s *pp42View) Get(k *database.Key) ([]byte, error) {
	// Mode 3 fails the owner's work on the captured snapshot: after the first four
	// reads (the read view's identity checks), every read and cursor errors.
	if s.reads++; s.mode == 3 && s.reads > 4 {
		return nil, errPP42ViewFault
	}
	s.slow()
	if s.mode == 1 && bytes.HasSuffix(k.Bucket().Path(), []byte("/acceptance-data/")) {
		return nil, database.ErrNotFound
	}
	if s.mode == 2 && !s.failed && bytes.HasSuffix(k.Bucket().Path(), []byte("/multisets/")) {
		s.failed = true
		return nil, database.ErrNotFound
	}
	return s.s.Get(k)
}
func (s *pp42View) Has(k *database.Key) (bool, error) { s.slow(); return s.s.Has(k) }

// slow delays a read of a mode 4 snapshot.
func (s *pp42View) slow() {
	if s.mode == 4 {
		s.owner.slowReads.Add(1)
		time.Sleep(pp42SlowRead)
	}
}
func (s *pp42View) Cursor(b *database.Bucket) (database.Cursor, error) {
	if s.mode == 3 {
		return nil, errPP42ViewFault
	}
	return s.s.Cursor(b)
}
func (s *pp42View) Close() error {
	e := s.s.Close()
	if e == nil {
		s.owner.open.Add(-1)
	}
	return e
}

func (d *pp42ViewDB) requireNoOpenSnapshot(t *testing.T) {
	t.Helper()
	if n := d.open.Load(); n != 0 {
		t.Fatalf("%d journal snapshots still open", n)
	}
}

// pp42RequireSameAsSource requires the old path's exact serialized set for point.
func (n *pruningMoveNode) pp42RequireSameAsSource(point *externalapi.DomainHash) {
	n.t.Helper()
	want, got := pp42SerializedSet(n.t, n.source, point), pp42SerializedSet(n.t, n.node, point)
	if len(got) != len(want) {
		n.t.Fatalf("set sizes %d != old path %d", len(got), len(want))
	}
	for i := range want {
		if !bytes.Equal(got[i], want[i]) {
			n.t.Fatalf("entry %d differs from the old path", i)
		}
	}
	n.requireServedAndVerified(point)
}

func newPP42PhaseNode(t *testing.T, cfg *consensus.Config, hook func(string)) (*pruningMoveNode, *pp42ViewDB, func()) {
	t.Helper()
	cfg.DisableDifficultyAdjustment = true
	cfg.PruningLegacyFixture = true
	t.Setenv("HTND_TEST_MODE", "0")
	t.Setenv("HTND_PEBBLE_DISABLE_WAL", "0")

	dir := t.TempDir()
	if override := os.Getenv("PP42_NODE_CRASH_DIR"); override != "" {
		dir = override
	}
	raw, e := pebble.OpenPebbleDB(dir, 8)
	if e != nil {
		t.Fatal(e)
	}
	db := &pp42ViewDB{DB: raw}
	n, closeNode := newPruningMoveNodeWithDatabase(t, cfg, "PP42Phase", true, db)
	n.node.Close()

	n.nodeConfig.PruningLegacyFixture = false
	n.nodeConfig.PruningPointJournalTestHook = hook
	n.node, _, e = consensus.NewFactory().NewConsensus(n.nodeConfig, db, n.dbPrefix, nil)
	if e != nil {
		closeNode()
		t.Fatal(e)
	}
	return n, db, closeNode
}
func TestPP42DerivationPhasesAllowForeground(t *testing.T) {
	for mode, phase := range []string{"derive-acceptance", "derive-diff-chain", "derive-bucket"} {
		t.Run(phase, func(t *testing.T) {
			testutils.ForAllNets(t, true, func(t *testing.T, cfg *consensus.Config) {
				defer constants.ForceSetBlockVersion(1)
				h := &scanHold{reached: make(chan struct{}), release: make(chan struct{}), closed: make(chan struct{})}
				var armed atomic.Bool
				n, db, closeNode := newPP42PhaseNode(t, cfg, func(p string) {
					if p == phase && armed.CompareAndSwap(true, false) {
						close(h.reached)
						<-h.release
					}
				})
				defer closeNode()
				defer h.Release()
				n.insertUntilThePruningPointMoves(n.insertAndUpdate, nil)
				n.waitUntilUpdated()
				db.mode.Store(int32(mode))
				armed.Store(true)
				target := n.insertUntilThePruningPointMoves(n.insertAndUpdate, nil)
				n.waitForHold(h)
				for i := 0; i < 20; i++ {
					if e := n.insertAndUpdate(n.nextBlock()); e != nil {
						t.Fatal(e)
					}
				}
				if e := n.within("template during derivation", func() error {
					_, e := n.node.BuildBlockTemplate(&externalapi.DomainCoinbaseData{ScriptPublicKey: &externalapi.ScriptPublicKey{Script: []byte{0x51}}}, nil)
					return e
				}); e != nil {
					t.Fatal(e)
				}
				n.requireRefused(target)
				if e := n.node.ValidateAndInsertImportedPruningPoint(target); e == nil {
					t.Fatal("active import bypassed owner")
				}
				h.Release()
				n.waitUntilUpdated()
				n.requireNoMismatch()
				n.pp42RequireSameAsSource(n.pruningPoint())
				db.requireNoOpenSnapshot(t)
			})
		})
	}
}

// Shutdown at each owner phase: Close joins the owner, every snapshot is released,
// no worker goroutine survives, the job stays pending, and a restart without the
// switch resumes it to the old path's exact set; the next move then also succeeds.
func TestPP42ShutdownRecovery(t *testing.T) {
	defer func(entries int) { pruningstore.JournalBatchEntries = entries }(pruningstore.JournalBatchEntries)
	pruningstore.JournalBatchEntries = 2 // "data-commit" then interrupts between apply batches
	for _, phase := range []string{"derive-acceptance", "journal-chunk", "ready", "data-commit", "complete", "scan"} {
		t.Run(phase, func(t *testing.T) {
			testutils.ForAllNets(t, true, func(t *testing.T, cfg *consensus.Config) {
				defer constants.ForceSetBlockVersion(1)
				h := &scanHold{reached: make(chan struct{}), release: make(chan struct{}), closed: make(chan struct{})}
				var armed atomic.Bool
				n, db, closeNode := newPP42PhaseNode(t, cfg, func(p string) {
					if p == phase && armed.CompareAndSwap(true, false) {
						close(h.reached)
						<-h.release
					}
				})
				defer closeNode()
				defer h.Release()
				n.insertUntilThePruningPointMoves(n.insertAndUpdate, nil)
				n.waitUntilUpdated()
				armed.Store(true)
				target := n.insertUntilThePruningPointMoves(n.insertAndUpdate, nil)
				n.waitForHold(h)
				closed := make(chan struct{})
				go func() { n.node.Close(); close(closed) }()
				select {
				case <-closed:
					t.Fatal("Close returned before owner released snapshot/batch")
				case <-time.After(20 * time.Millisecond):
				}
				h.Release()
				select {
				case <-closed:
				case <-time.After(10 * time.Second):
					t.Fatal("Close did not join")
				}
				if !n.updating() {
					t.Fatal("interrupted job cleared pending")
				}
				db.requireNoOpenSnapshot(t)
				pp42RequireNoWorkerGoroutine(t)
				if phase == "data-commit" { // resume must start from a durable partial cursor
					data, e := db.Get(pp42StateKey())
					var state pruningstore.JournalState
					if e == nil {
						e = json.Unmarshal(data, &state)
					}
					if e != nil || state.Phase != "ready" || state.Applied == 0 || state.Applied >= state.Count {
						t.Fatalf("shutdown inside apply left %+v, %v", state, e)
					}
				}
				n.nodeConfig.PruningPointJournalTestHook = nil
				// Persistence, rather than environment, selects recovery after restart.

				var e error
				n.node, _, e = consensus.NewFactory().NewConsensus(n.nodeConfig, db, n.dbPrefix, nil)
				if e != nil {
					t.Fatal(e)
				}
				n.waitUntilUpdated()
				n.pp42RequireSameAsSource(target)
				n.requireNoMismatch()
				next := n.insertUntilThePruningPointMoves(n.insertAndUpdate, nil)
				n.waitUntilUpdated()
				n.pp42RequireSameAsSource(next)
				n.node.Close()
				db.requireNoOpenSnapshot(t)
				if e = db.Close(); e != nil { // Pebble refuses to close with a leaked snapshot
					t.Fatal(e)
				}
			})
		})
	}
}

// A derivation error releases the snapshot, leaves the job pending in PREPARING with
// the live set untouched, keeps foreground work going, and the next request retries.
func TestPP42DerivationErrorReleasesSnapshot(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, cfg *consensus.Config) {
		defer constants.ForceSetBlockVersion(1)
		derived := make(chan struct{}, 16)
		n, db, closeNode := newPP42PhaseNode(t, cfg, func(p string) {
			if p == "derive" {
				select {
				case derived <- struct{}{}:
				default:
				}
			}
		})
		defer closeNode()
		n.insertUntilThePruningPointMoves(n.insertAndUpdate, nil)
		n.waitUntilUpdated()
		for len(derived) > 0 {
			<-derived
		}
		db.mode.Store(3)
		target := n.insertUntilThePruningPointMoves(n.insertAndUpdate, nil)
		select {
		case <-derived:
		case <-time.After(scanTestTimeout):
			t.Fatal("derivation not attempted")
		}
		waitUntil(t, "the failed derivation released its snapshot", func() bool { return db.open.Load() == 0 })
		state, e := db.Get(pp42StateKey())
		if e != nil || !bytes.Contains(state, []byte(`"Phase":"preparing"`)) || !n.updating() {
			t.Fatalf("failed derivation left %s, pending %t, %v", state, n.updating(), e)
		}
		n.requireRefused(target)
		for i := 0; i < 5; i++ {
			if e := n.insertAndUpdate(n.nextBlock()); e != nil {
				t.Fatal(e)
			}
		}
		db.mode.Store(0)
		if e := n.insertAndUpdate(n.nextBlock()); e != nil {
			t.Fatal(e)
		}
		n.waitUntilUpdated()
		n.requireNoMismatch()
		n.pp42RequireSameAsSource(n.pruningPoint())
		n.node.Close()
		db.requireNoOpenSnapshot(t)
		pp42RequireNoWorkerGoroutine(t)
	})
}

// The declared disk policy (pruningstore.JournalDiskPolicy) refuses before writing.
// In-flight work waits resumably, also across a restart, while foreground continues.
// Without in-flight work a refusal keeps, or normalizes back to, the synchronous
// path, which keeps moving the pruning point; enough space re-enables the journal.
func TestPP42DiskReserveRefusal(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, cfg *consensus.Config) {
		defer constants.ForceSetBlockVersion(1)
		var fallbacks atomic.Int32
		hold := pp42NewScanHold()
		n, db, closeNode := newPP42PhaseNode(t, cfg, func(p string) {
			if p == "fallback" {
				fallbacks.Add(1)
			}
			hold.hook(p)
		})
		defer closeNode()
		defer hold.release()
		n.insertUntilThePruningPointMoves(n.insertAndUpdate, nil)
		n.waitUntilUpdated()
		marker := func() []byte {
			state, e := db.Get(pp42StateKey())
			if database.IsNotFoundError(e) {
				return nil
			}
			if e != nil {
				t.Fatal(e)
			}
			return state
		}
		reopen := func() {
			t.Helper()
			n.node.Close()
			var e error
			n.node, _, e = consensus.NewFactory().NewConsensus(n.nodeConfig, db, n.dbPrefix, nil)
			if e != nil {
				t.Fatal(e)
			}
		}

		// Owner decision D1: the move right after the COMPLETE base, refused before
		// PREPARING, runs once on the synchronous path; its finish deletes history.
		base := n.pruningPoint()
		db.avail.Store(1)
		target := n.insertUntilThePruningPointMoves(n.insertAndUpdate, nil)
		n.waitUntilUpdated()
		n.pp42RequireSameAsSource(target)
		n.pp42RequireHistoryAsSource(base, target)
		if state := pp42Manifest(t, db.DB); state == nil || state.Phase != "complete" || state.Target != target.String() || state.Bytes == 0 {
			t.Fatalf("manifest after the fallback %+v, want complete at %s with the previous job size", state, target)
		}
		if got := fallbacks.Load(); got != 1 {
			t.Fatalf("%d synchronous fallbacks, want 1", got)
		}
		// No loop: nothing is pending, so further blocks write nothing again.
		for i := 0; i < 20; i++ {
			if e := n.insertAndUpdate(n.nextBlock()); e != nil {
				t.Fatal(e)
			}
		}
		if _, e := n.node.BuildBlockTemplate(&externalapi.DomainCoinbaseData{ScriptPublicKey: &externalapi.ScriptPublicKey{Script: []byte{0x51}}}, nil); e != nil {
			t.Fatal(e)
		}
		if got := fallbacks.Load(); got != 1 || n.updating() {
			t.Fatalf("after the fallback: %d fallbacks, pending %t", got, n.updating())
		}
		n.requireNoMismatch()

		// The backlog guard: two moves behind, the refused job keeps waiting. The
		// legacy diff of the current move does not fit the older set, so neither the
		// fallback nor a legacy write runs, also not after a restart while short.
		db.avail.Store(0)
		hold.arm()
		behind := n.insertUntilThePruningPointMoves(n.insertAndUpdate, nil)
		hold.wait(t)
		n.insertUntilThePruningPointMoves(n.insertAndUpdate, nil)
		current := n.insertUntilThePruningPointMoves(n.insertAndUpdate, nil)
		db.avail.Store(1)
		queries := db.queries.Load()
		hold.release()
		waitUntil(t, "the backlogged job checked the disk policy", func() bool { return db.queries.Load() > queries })
		requireWaiting := func(what string) {
			t.Helper()
			queries := db.queries.Load()
			for i := 0; i < 5; i++ {
				if e := n.insertAndUpdate(n.nextBlock()); e != nil {
					t.Fatal(e)
				}
			}
			waitUntil(t, "the waiting job was retried", func() bool { return db.queries.Load() > queries })
			if state := pp42Manifest(t, db.DB); state == nil || state.Phase != "complete" || state.Target != behind.String() || !n.updating() {
				t.Fatalf("%s: manifest %+v, pending %t; want complete at %s, pending", what, state, n.updating(), behind)
			}
			if got := fallbacks.Load(); got != 1 {
				t.Fatalf("%s: the fallback ran in a backlog (%d)", what, got)
			}
			n.requireRefused(current)
			n.requireNoMismatch()
		}
		requireWaiting("backlog")
		// A restart while still short of space must start, not fail closed.
		reopen()
		requireWaiting("backlog after a restart")
		db.avail.Store(0)
		if e := n.insertAndUpdate(n.nextBlock()); e != nil {
			t.Fatal(e)
		}
		n.waitUntilUpdated()
		n.pp42RequireSameAsSource(n.pruningPoint())
		if got := fallbacks.Load(); got != 1 {
			t.Fatalf("catch-up used the fallback (%d)", got)
		}

		// Idle refusal at startup normalizes back to the synchronous path.
		n.nodeConfig.PruningJournalDiskFloorBytes = 1 << 62
		reopen()
		if state := marker(); state != nil {
			t.Fatalf("idle refusal kept the journal: %s", state)
		}
		next := n.insertUntilThePruningPointMoves(n.insertAndUpdate, nil)
		n.waitUntilUpdated()
		n.pp42RequireSameAsSource(next)
		// Without any marker, refusal keeps the synchronous path.
		reopen()
		next = n.insertUntilThePruningPointMoves(n.insertAndUpdate, nil)
		n.waitUntilUpdated()
		n.pp42RequireSameAsSource(next)
		if state := marker(); state != nil {
			t.Fatalf("synchronous path wrote a journal marker: %s", state)
		}
		// With the declared default policy the journal is re-enabled and measures jobs.
		n.nodeConfig.PruningJournalDiskFloorBytes = 0
		reopen()
		next = n.insertUntilThePruningPointMoves(n.insertAndUpdate, nil)
		n.waitUntilUpdated()
		n.pp42RequireSameAsSource(next)
		if state := marker(); !bytes.Contains(state, []byte(`"Phase":"complete"`)) || bytes.Contains(state, []byte(`"Bytes":0`)) {
			t.Fatalf("re-enabled journal manifest %s", state)
		}
		n.requireNoMismatch()
		n.node.Close()
		db.requireNoOpenSnapshot(t)
	})
}

// pp42ScanHold parks the journal owner at its next "scan" hook, after a job is
// COMPLETE and before its check, so moves can be selected behind it.
type pp42ScanHold struct {
	armed            atomic.Bool
	reached, resumed chan struct{}
	once             sync.Once
}

func pp42NewScanHold() *pp42ScanHold {
	return &pp42ScanHold{reached: make(chan struct{}), resumed: make(chan struct{})}
}
func (h *pp42ScanHold) arm() { h.armed.Store(true) }
func (h *pp42ScanHold) hook(p string) {
	if p == "scan" && h.armed.CompareAndSwap(true, false) {
		close(h.reached)
		<-h.resumed
	}
}
func (h *pp42ScanHold) wait(t *testing.T) {
	t.Helper()
	select {
	case <-h.reached:
	case <-time.After(30 * time.Second):
		t.Fatal("the journal owner never reached its scan")
	}
}
func (h *pp42ScanHold) release() { h.once.Do(func() { close(h.resumed) }) }

// pp42RequireHistoryAsSource requires each selected-chain block strictly between from
// and to to have the old path's status, and at least one of them deleted.
func (n *pruningMoveNode) pp42RequireHistoryAsSource(from, to *externalapi.DomainHash) {
	n.t.Helper()
	chain, e := n.source.GetVirtualSelectedParentChainFromBlock(from)
	if e != nil {
		n.t.Fatal(e)
	}
	deleted := 0
	for _, hash := range chain.Added {
		if hash.Equal(to) {
			break
		}
		got, e := n.node.GetBlockInfo(hash)
		if e != nil {
			n.t.Fatal(e)
		}
		want, e := n.source.GetBlockInfo(hash)
		if e != nil {
			n.t.Fatal(e)
		}
		if got.BlockStatus != want.BlockStatus {
			n.t.Fatalf("block %s: status %s, old path %s", hash, got.BlockStatus, want.BlockStatus)
		}
		if got.BlockStatus == externalapi.StatusHeaderOnly {
			deleted++
		}
	}
	if deleted == 0 {
		n.t.Fatalf("no history between %s and %s was deleted", from, to)
	}
}

// A sealed (READY) job needs only the sealed floor: it proceeds at a free-space level
// where an unsealed job is refused, so the job and its history deletion can free
// disk. Below the sealed floor it waits resumably and resumes when space returns.
func TestPP42SealedJobDiskFloor(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, cfg *consensus.Config) {
		defer constants.ForceSetBlockVersion(1)
		const floor = 1 << 30
		var atReady atomic.Uint64 // free bytes to report once the next job is sealed
		var db *pp42ViewDB
		var fallbacks atomic.Int32
		n, db, closeNode := newPP42PhaseNode(t, cfg, func(p string) {
			if p == "fallback" {
				fallbacks.Add(1)
			}
			if p != "ready" {
				return
			}
			if v := atReady.Swap(0); v != 0 {
				db.avail.Store(v)
			}
		})
		defer closeNode()
		marker := func() []byte {
			state, e := db.Get(pp42StateKey())
			if e != nil {
				t.Fatal(e)
			}
			return state
		}
		n.insertUntilThePruningPointMoves(n.insertAndUpdate, nil)
		n.waitUntilUpdated()
		if bytes.Contains(marker(), []byte(`"Bytes":0`)) {
			t.Fatalf("first job size not measured: %s", marker())
		}

		// Sealed at floor+1: below floor + 4 x job bytes, yet apply, scan and finish run.
		atReady.Store(floor + 1)
		n.insertUntilThePruningPointMoves(n.insertAndUpdate, nil)
		n.waitUntilUpdated()
		n.pp42RequireSameAsSource(n.pruningPoint())
		if db.avail.Load() != floor+1 {
			t.Fatal("the sealed job did not run at the reduced free-space level")
		}
		// At the same level the next, unsealed job is refused before PREPARING. It is
		// the move right after the COMPLETE base, so it runs on the synchronous path
		// instead (owner decision D1), once.
		target := n.insertUntilThePruningPointMoves(n.insertAndUpdate, nil)
		n.waitUntilUpdated()
		n.pp42RequireSameAsSource(target)
		if got := fallbacks.Load(); got != 1 || db.avail.Load() != floor+1 {
			t.Fatalf("unsealed job at the sealed level: %d fallbacks at %d free bytes, want 1 at %d", got, db.avail.Load(), floor+1)
		}
		db.avail.Store(0)

		// Below the sealed floor the sealed job waits; foreground continues.
		atReady.Store(floor - 1)
		target = n.insertUntilThePruningPointMoves(n.insertAndUpdate, nil)
		waitUntil(t, "the sealed job was refused", func() bool {
			return db.avail.Load() == floor-1 && bytes.Contains(marker(), []byte(`"Phase":"ready"`))
		})
		queries := db.queries.Load()
		for i := 0; i < 5; i++ {
			if e := n.insertAndUpdate(n.nextBlock()); e != nil {
				t.Fatal(e)
			}
		}
		waitUntil(t, "the waiting job was retried", func() bool { return db.queries.Load() > queries })
		if state := marker(); !bytes.Contains(state, []byte(`"Phase":"ready"`)) || !bytes.Contains(state, []byte(`"Applied":0`)) || !n.updating() {
			t.Fatalf("waiting sealed job: manifest %s, pending %t", state, n.updating())
		}
		n.requireRefused(target)
		// Space returns: the same sealed job resumes and finishes.
		db.avail.Store(0)
		if e := n.insertAndUpdate(n.nextBlock()); e != nil {
			t.Fatal(e)
		}
		n.waitUntilUpdated()
		n.pp42RequireSameAsSource(n.pruningPoint())
		n.requireNoMismatch()
		n.node.Close()
		db.requireNoOpenSnapshot(t)
	})
}

func TestPP42CleanDowngradeAndReupgrade(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, cfg *consensus.Config) {
		defer constants.ForceSetBlockVersion(1)
		n, db, closeNode := newPP42PhaseNode(t, cfg, nil)
		defer closeNode()
		n.insertUntilThePruningPointMoves(n.insertAndUpdate, nil)
		n.waitUntilUpdated()
		normalizer, ok := n.node.(interface{ PreparePruningPointDowngrade(context.Context) error })
		if !ok {
			t.Fatal("normalizer unavailable")
		}
		if e := normalizer.PreparePruningPointDowngrade(context.Background()); e != nil {
			t.Fatal(e)
		}
		key := database.MakeBucket(n.dbPrefix.Serialize()).Key([]byte("pruning-point-utxo-set-journal-state"))
		if found, e := db.Has(key); e != nil || found {
			t.Fatal("marker survived", e)
		}

		var e error
		n.node, _, e = consensus.NewFactory().NewConsensus(n.nodeConfig, db, n.dbPrefix, nil)
		if e != nil {
			t.Fatal(e)
		}
		n.requireServedAndVerified(n.pruningPoint())
		n.insertUntilThePruningPointMoves(n.insertAndUpdate, nil)
		n.waitUntilUpdated()
		n.node.Close()

		n.node, _, e = consensus.NewFactory().NewConsensus(n.nodeConfig, db, n.dbPrefix, nil)
		if e != nil {
			t.Fatal(e)
		}
		n.requireServedAndVerified(n.pruningPoint())
		n.insertUntilThePruningPointMoves(n.insertAndUpdate, nil)
		n.waitUntilUpdated()
		n.requireNoMismatch()
	})
}

func TestPP42ActualOldBinaryDowngrade(t *testing.T) {
	executable := os.Getenv("PP42_BASE_TEST_BINARY")
	if executable == "" {
		t.Skip("set PP42_BASE_TEST_BINARY to the separately built, pinned legacy-consensus probe")
	}
	defer constants.ForceSetBlockVersion(1)
	dir := t.TempDir()
	t.Setenv("PP42_NODE_CRASH_DIR", dir)
	cfg := pp42CrashConfig()
	cfg.DeferPruningPointUTXOSetUpdate = false
	n, db, closeNode := newPP42PhaseNode(t, cfg, nil)
	defer closeNode()
	target := n.insertUntilThePruningPointMoves(n.insertAndUpdate, nil)
	n.waitUntilUpdated()
	expected := pp42Expected{target.String(), pp42SerializedSet(t, n.node, target)}
	data, e := json.Marshal(expected)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(dir, "expected.json"), data, 0600); e != nil {
		t.Fatal(e)
	}
	if e = n.node.(interface{ PreparePruningPointDowngrade(context.Context) error }).PreparePruningPointDowngrade(context.Background()); e != nil {
		t.Fatal(e)
	}
	if e = db.Close(); e != nil {
		t.Fatal(e)
	}
	pp42RunLegacyProbe(t, executable, dir)
	raw, e := pebble.OpenPebbleDB(dir, 8)
	if e != nil {
		t.Fatal(e)
	}
	db.DB = raw
	db.closed.Store(false)
	n.node, _, e = consensus.NewFactory().NewConsensus(n.nodeConfig, raw, n.dbPrefix, nil)
	if e != nil {
		t.Fatal(e)
	}
	n.requireServedAndVerified(n.pruningPoint())
	n.requireNoMismatch()
	// Re-upgrade: the journal re-initializes on the old binary's state and moves again.
	pp42MineMove(t, raw, n.node)
	if found, e := raw.Has(pp42StateKey()); e != nil || !found {
		t.Fatalf("re-upgraded node has no journal marker: %v", e)
	}
}

// PREP B2: re-upgrade after the previous release ran on a database this release left
// un-normalized (no --prepare-downgrade) and moved the pruning point itself. The new
// binary must start with no operator step, re-initialise its journal from the finished
// legacy set, serve exactly that set (every entry of the legacy bucket, and a verified
// commitment) and journal the next move. journal-move: the last move before the
// downgrade was a journal job; initialized: the journal was enabled on a legacy-moved
// database and the node stopped before its first job. corrupt: as initialized, but the
// legacy set the old binary left is damaged; re-initialising must not bypass the #41
// check, so the first journal move's full scan halts and the update stays pending.
func TestPP42UnnormalizedOldBinaryReupgrade(t *testing.T) {
	executable := os.Getenv("PP42_BASE_TEST_BINARY")
	if executable == "" {
		t.Skip("set PP42_BASE_TEST_BINARY to the separately built, pinned legacy-consensus probe")
	}
	defer constants.ForceSetBlockVersion(1)
	for _, variant := range pp42UnnormalizedVariants {
		t.Run(variant, func(t *testing.T) {
			if variant == "own-backlog" {
				pp42OwnBacklogRestart(t)
				return
			}
			dir := t.TempDir()
			t.Setenv("PP42_NODE_CRASH_DIR", dir)
			n, db, closeNode := newPP42PhaseNode(t, pp42CrashConfig(), nil)
			defer closeNode()
			reopen := func(journal string) {
				n.nodeConfig.PruningLegacyFixture = journal == "0"
				var e error
				if n.node, _, e = consensus.NewFactory().NewConsensus(n.nodeConfig, db, n.dbPrefix, nil); e != nil {
					t.Fatal(e)
				}
			}
			if variant != "journal-move" && !strings.HasPrefix(variant, "stopped-") {
				// A database from before the upgrade: no marker, legacy moves.
				n.node.Close()
				if e := db.Delete(pp42StateKey()); e != nil {
					t.Fatal(e)
				}
				reopen("0")
			}
			target := n.insertUntilThePruningPointMoves(n.insertAndUpdate, nil)
			n.waitUntilUpdated()
			if variant != "journal-move" && !strings.HasPrefix(variant, "stopped-") {
				n.node.Close()
				reopen("1")
			}
			manifest := pp42Manifest(t, db.DB)
			if manifest == nil || manifest.Phase != "complete" || manifest.Target != target.String() {
				t.Fatalf("manifest before the downgrade %+v, want complete at %s", manifest, target)
			}
			data, e := json.Marshal(pp42Expected{target.String(), pp42SerializedSet(t, n.node, target)})
			if e != nil {
				t.Fatal(e)
			}
			if e = os.WriteFile(filepath.Join(dir, "expected.json"), data, 0600); e != nil {
				t.Fatal(e)
			}
			n.node.Close()
			if e = db.Close(); e != nil {
				t.Fatal(e)
			}
			pp42RunLegacyProbe(t, executable, dir)
			raw, e := pebble.OpenPebbleDB(dir, 8)
			if e != nil {
				t.Fatal(e)
			}
			db.DB = raw
			db.closed.Store(false)
			if strings.HasPrefix(variant, "stopped-") {
				// PANEL-42b D2: the old binary finished its moves, then stopped inside the
				// next one. The re-upgrade must finish that move to the old path's exact
				// set without a mismatch or a per-block retry, then journal the move after.
				want, commitment := pp42OldBinaryStoppedInNextMove(t, n, db, dir, variant == "stopped-mid-write")
				reopen("1")
				pp42WaitDrained(t, db.DB)
				point := n.pruningPoint()
				if point.String() != want.Target {
					t.Fatalf("pruning point %s after the re-upgrade, the stopped move's is %s", point, want.Target)
				}
				pp42RequireSet(t, n.node, point, want.Entries, commitment)
				n.requireNoMismatch()
				pp42MineMove(t, db.DB, n.node)
				if after := pp42Manifest(t, db.DB); after == nil || after.Phase != "complete" || after.Target != n.pruningPoint().String() {
					t.Fatalf("the move after re-upgrade was not journaled: %+v", after)
				}
				return
			}
			if variant == "corrupt" {
				n.corruptPruningPointUTXOSet()
			}
			legacy := pp42LegacyBucketEntries(t, raw)
			n.node, _, e = consensus.NewFactory().NewConsensus(n.nodeConfig, raw, n.dbPrefix, nil)
			if e != nil {
				t.Fatalf("re-upgrade over the un-normalized old-binary run refused: %v", e)
			}
			point := n.pruningPoint()
			if point.Equal(target) {
				t.Fatal("the old binary did not move the pruning point")
			}
			if after := pp42Manifest(t, raw); after == nil || after.Phase != "complete" || after.Target != point.String() ||
				after.TargetIndex <= manifest.TargetIndex {
				t.Fatalf("manifest after re-upgrade %+v, want complete at %s", after, point)
			}
			if served := pp42SerializedSet(t, n.node, point); len(served) != legacy {
				t.Fatalf("served %d entries, the old binary's finished legacy bucket has %d", len(served), legacy)
			}
			if variant == "corrupt" {
				// Mined on the old binary's chain, as pp42MineMove does, without waiting to drain.
				for i := 0; n.pruningPoint().Equal(point); i++ {
					if i == 300 {
						t.Fatal("pruning point did not move")
					}
					template, e := n.node.BuildBlockTemplate(&externalapi.DomainCoinbaseData{ScriptPublicKey: &externalapi.ScriptPublicKey{Script: []byte{0x51}}}, nil)
					if e != nil {
						t.Fatal(e)
					}
					if e = n.node.ValidateAndInsertBlock(template.Block, true, true); e != nil {
						t.Fatal(e)
					}
					if e = n.node.UpdatePruningPointIfRequired(); e != nil {
						t.Fatal(e)
					}
				}
				select {
				case <-n.mismatches:
				case <-time.After(10 * time.Second):
					t.Fatal("the corrupt re-initialised set passed the next move's check")
				}
				if !n.updating() {
					t.Fatal("the mismatch cleared pending")
				}
				return
			}
			n.requireServedAndVerified(point)
			n.requireNoMismatch()
			pp42MineMove(t, raw, n.node)
			if after := pp42Manifest(t, raw); after == nil || after.Target != n.pruningPoint().String() {
				t.Fatalf("the move after re-upgrade was not journaled: %+v", after)
			}
		})
	}
}

// pp42UnnormalizedVariants are the re-upgrade cases this arm's old binary can serve,
// plus the own-backlog restart control.
var pp42UnnormalizedVariants = []string{"journal-move", "initialized", "corrupt", "stopped-mid-write",
	"stopped-before-scan", "own-backlog"}

// pp42OldBinaryStoppedInNextMove leaves db as the previous release leaves it when it
// is stopped inside a pruning point move after the pinned probe finished its own:
// the move selected (pending), its diff method recorded, and the diff written
// completely (stopped between its write and its scan) or partly (stopped mid-write).
// The pinned probe cannot be stopped there, so the stop is constructed: this build's
// legacy path (journal off, the marker set aside) selects the move without writing,
// a copy of the database finishes the move on the same path as the reference, and
// the method record and diff are then written into the original key by key, as the
// legacy writer does (deletes, then puts). The stale journal marker is put back.
// It returns the reference set and commitment of the stopped move.
func pp42OldBinaryStoppedInNextMove(t *testing.T, n *pruningMoveNode, db *pp42ViewDB, dir string, midWrite bool) (pp42Expected, string) {
	t.Helper()
	marker, e := db.DB.Get(pp42StateKey())
	if e != nil {
		t.Fatal(e)
	}
	if e = db.DB.Delete(pp42StateKey()); e != nil {
		t.Fatal(e)
	}

	before := pp42LegacyBucket(t, db.DB)
	legacyConfig := *n.nodeConfig
	legacyConfig.PruningLegacyFixture = true
	node, _, e := consensus.NewFactory().NewConsensus(&legacyConfig, db, n.dbPrefix, nil)
	if e != nil {
		t.Fatal(e)
	}
	start, e := node.PruningPoint()
	if e != nil {
		t.Fatal(e)
	}
	next := start
	for i := 0; next.Equal(start); i++ {
		if i == 300 {
			t.Fatal("pruning point did not move")
		}
		template, e := node.BuildBlockTemplate(&externalapi.DomainCoinbaseData{ScriptPublicKey: &externalapi.ScriptPublicKey{Script: []byte{0x51}}}, nil)
		if e != nil {
			t.Fatal(e)
		}
		// No UpdatePruningPointIfRequired: the move is selected, its set not written.
		if e = node.ValidateAndInsertBlock(template.Block, true, true); e != nil {
			t.Fatal(e)
		}
		if next, e = node.PruningPoint(); e != nil {
			t.Fatal(e)
		}
	}
	node.Close()
	methodKey := database.MakeBucket(n.dbPrefix.Serialize()).Key([]byte("pruning-point-utxo-set-update-method"))
	if found, e := db.DB.Has(methodKey); e != nil || found || !n.updating() || len(pp42LegacyBucket(t, db.DB)) != len(before) {
		t.Fatalf("selection only: method recorded %t (%v), pending %t", found, e, n.updating())
	}
	ref := t.TempDir()
	if e = db.DB.Close(); e != nil {
		t.Fatal(e)
	}
	if out, e := exec.Command("cp", "-R", dir+"/.", ref).CombinedOutput(); e != nil {
		t.Fatalf("copy: %v %s", e, out)
	}

	// Reference: the same legacy path finishes the move on the copy.
	var phases sync.Map
	cfg := legacyConfig
	cfg.PruningPointJournalTestHook = func(p string) { phases.Store(p, true) }
	refRaw, e := pebble.OpenPebbleDB(ref, 8)
	if e != nil {
		t.Fatal(e)
	}
	refNode, _, e := consensus.NewFactory().NewConsensus(&cfg, refRaw, n.dbPrefix, nil)
	if e != nil {
		t.Fatal(e)
	}
	if e = refNode.UpdatePruningPointIfRequired(); e != nil {
		t.Fatal(e)
	}
	pp42WaitDrained(t, refRaw)
	if _, acceptance := phases.Load("derive-acceptance"); !acceptance {
		t.Fatal("the reference did not derive from acceptance data")
	}
	if _, chain := phases.Load("derive-diff-chain"); chain {
		t.Fatal("the reference also derived a diff-chain diff: the recorded method is not known")
	}
	want := pp42Expected{next.String(), pp42SerializedSet(t, refNode, next)}
	commitment := pp42Commitment(t, refNode, next)
	after := pp42LegacyBucket(t, refRaw)
	refNode.Close()
	if e = refRaw.Close(); e != nil {
		t.Fatal(e)
	}

	raw, e := pebble.OpenPebbleDB(dir, 8)
	if e != nil {
		t.Fatal(e)
	}
	db.DB = raw
	db.closed.Store(false)
	var deletes, puts []string
	for k := range before {
		if _, ok := after[k]; !ok {
			deletes = append(deletes, k)
		}
	}
	for k, v := range after {
		if old, ok := before[k]; !ok || !bytes.Equal(old, v) {
			puts = append(puts, k)
		}
	}
	sort.Strings(deletes)
	sort.Strings(puts)
	if len(deletes)+len(puts) < 2 {
		t.Fatalf("the move's diff has %d writes, too few to stop inside", len(deletes)+len(puts))
	}
	if midWrite {
		if len(puts) > 0 {
			puts = puts[:len(puts)/2]
		} else {
			deletes = deletes[:len(deletes)/2]
		}
	}
	if e = raw.Put(methodKey, append(next.ByteSlice(), []byte("acceptance-data")...)); e != nil {
		t.Fatal(e)
	}
	bucket := database.MakeBucket(n.dbPrefix.Serialize()).Bucket([]byte("pruning-point-utxo-set"))
	for _, k := range deletes {
		if e = raw.Delete(bucket.Key([]byte(k))); e != nil {
			t.Fatal(e)
		}
	}
	for _, k := range puts {
		if e = raw.Put(bucket.Key([]byte(k)), after[k]); e != nil {
			t.Fatal(e)
		}
	}
	got, complete := pp42LegacyBucket(t, raw), true
	for k, v := range after {
		complete = complete && bytes.Equal(got[k], v)
	}
	if complete = complete && len(got) == len(after); complete == midWrite {
		t.Fatalf("constructed legacy bucket complete %t, want %t", complete, !midWrite)
	}
	if e = raw.Put(pp42StateKey(), marker); e != nil {
		t.Fatal(e)
	}
	t.Logf("stopped inside move %s: method recorded, %d deletes and %d puts of the diff written, stale manifest %s",
		next, len(deletes), len(puts), marker)
	return want, commitment
}

// pp42LegacyBucket reads the legacy pruning point UTXO bucket as key suffix → value.
func pp42LegacyBucket(t *testing.T, raw *pebble.DB) map[string][]byte {
	t.Helper()
	c, e := raw.Cursor(database.MakeBucket((&prefix.Prefix{}).Serialize()).Bucket([]byte("pruning-point-utxo-set")))
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	set := map[string][]byte{}
	for ok := c.First(); ok; ok = c.Next() {
		k, e := c.Key()
		if e != nil {
			t.Fatal(e)
		}
		v, e := c.Value()
		if e != nil {
			t.Fatal(e)
		}
		set[string(k.Suffix())] = append([]byte(nil), v...)
	}
	return set
}

// pp42OwnBacklogRestart is the control for the D2 variants: this node's own backlog
// (manifest COMPLETE two moves behind, pending, history intact) survives a restart
// and is replayed to the old path's exact set. The journal is refused for disk space
// across the restart, so the backlog cannot drain before it.
func pp42OwnBacklogRestart(t *testing.T) {
	hold := pp42NewScanHold()
	cfg := pp42CrashConfig()
	cfg.DeferPruningPointUTXOSetUpdate = false // the source is the old synchronous path
	n, db, closeNode := newPP42PhaseNode(t, cfg, hold.hook)
	defer closeNode()
	defer hold.release()
	n.insertUntilThePruningPointMoves(n.insertAndUpdate, nil)
	n.waitUntilUpdated()
	hold.arm()
	behind := n.insertUntilThePruningPointMoves(n.insertAndUpdate, nil)
	hold.wait(t)
	n.insertUntilThePruningPointMoves(n.insertAndUpdate, nil)
	current := n.insertUntilThePruningPointMoves(n.insertAndUpdate, nil)
	db.avail.Store(1)
	hold.release()
	n.node.Close()

	var e error
	if n.node, _, e = consensus.NewFactory().NewConsensus(n.nodeConfig, db, n.dbPrefix, nil); e != nil {
		t.Fatal(e)
	}
	if state := pp42Manifest(t, db.DB); state == nil || state.Phase != "complete" || state.Target != behind.String() || !n.updating() {
		t.Fatalf("own backlog after the restart: manifest %+v, pending %t; want complete at %s", state, n.updating(), behind)
	}
	n.requireRefused(current)
	db.avail.Store(0)
	if e = n.insertAndUpdate(n.nextBlock()); e != nil {
		t.Fatal(e)
	}
	n.waitUntilUpdated()
	n.pp42RequireSameAsSource(n.pruningPoint())
	n.requireNoMismatch()
	next := n.insertUntilThePruningPointMoves(n.insertAndUpdate, nil)
	n.waitUntilUpdated()
	n.pp42RequireSameAsSource(next)
	if state := pp42Manifest(t, db.DB); state == nil || state.Target != next.String() {
		t.Fatalf("the move after the backlog was not journaled: %+v", state)
	}
}

func pp42Manifest(t *testing.T, raw *pebble.DB) *pruningstore.JournalState {
	t.Helper()
	data, e := raw.Get(pp42StateKey())
	if database.IsNotFoundError(e) {
		return nil
	}
	if e != nil {
		t.Fatal(e)
	}
	state := new(pruningstore.JournalState)
	if e = json.Unmarshal(data, state); e != nil {
		t.Fatal(e)
	}
	return state
}

// pp42LegacyBucketEntries counts the legacy pruning point UTXO bucket, the set the
// previous release writes and serves.
func pp42LegacyBucketEntries(t *testing.T, raw *pebble.DB) int {
	t.Helper()
	c, e := raw.Cursor(database.MakeBucket((&prefix.Prefix{}).Serialize()).Bucket([]byte("pruning-point-utxo-set")))
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	count := 0
	for ok := c.First(); ok; ok = c.Next() {
		count++
	}
	return count
}

// pp42RunLegacyProbe runs the separately built, pinned baseline consensus probe on dir
// (closed by the caller) and requires its checkpoint test to actually pass: it serves
// expected.json's set, moves the pruning point, drains, reopens and verifies.
func pp42RunLegacyProbe(t *testing.T, executable, dir string) {
	t.Helper()
	cmd := exec.Command(executable, "-test.run=^TestPP42LegacyCheckpoint$", "-test.timeout=120s", "-test.v")
	cmd.Env = append(os.Environ(), "PP42_LEGACY_DIR="+dir)
	output, e := cmd.CombinedOutput()
	t.Logf("actual pinned legacy-consensus probe:\n%s", output)
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Contains(output, []byte("--- PASS: TestPP42LegacyCheckpoint (")) {
		t.Fatal("legacy executable did not actually pass the required checkpoint test")
	}
}

// pp42MineMove mines templates on node until its pruning point moves, then requires
// the update to drain and the served set to verify against the new commitment.
func pp42MineMove(t *testing.T, raw *pebble.DB, node externalapi.Consensus) {
	t.Helper()
	start, e := node.PruningPoint()
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; ; i++ {
		if i == 300 {
			t.Fatal("pruning point did not move")
		}
		template, e := node.BuildBlockTemplate(&externalapi.DomainCoinbaseData{ScriptPublicKey: &externalapi.ScriptPublicKey{Script: []byte{0x51}}}, nil)
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
			break
		}
	}
	pp42WaitDrained(t, raw)
	point, e := node.PruningPoint()
	if e != nil {
		t.Fatal(e)
	}
	health, e := node.CheckUTXOHealth(point)
	if e != nil || !health.Ready || !health.Verified {
		t.Fatalf("health after move %+v %v", health, e)
	}
}

// Downgrade from an unfinished job: kill the new binary inside a job, reopen it (its
// startup recovery finishes the job), normalize with PreparePruningPointDowngrade,
// close, then run the actual pinned old implementation on that database. Opening an
// unfinished, un-normalized database with the old binary is not claimed supported.
func TestPP42ActualOldBinaryAfterUnfinishedJob(t *testing.T) {
	executable := os.Getenv("PP42_BASE_TEST_BINARY")
	if executable == "" {
		t.Skip("set PP42_BASE_TEST_BINARY to the separately built, pinned legacy-consensus probe")
	}
	defer constants.ForceSetBlockVersion(1)
	for _, point := range []string{"preparing", "ready", "data-commit:2", "scan", "before-finish"} {
		t.Run(point, func(t *testing.T) {
			dir, want := pp42CrashAt(t, point)
			raw, node := pp42OpenRecovered(t, dir, want)
			if e := node.(interface{ PreparePruningPointDowngrade(context.Context) error }).PreparePruningPointDowngrade(context.Background()); e != nil {
				t.Fatal(e)
			}
			if found, e := raw.Has(pp42StateKey()); e != nil || found {
				t.Fatalf("normalization left the journal marker: %v", e)
			}
			pp42RequireNoWorkerGoroutine(t)
			if e := raw.Close(); e != nil {
				t.Fatal(e)
			}
			pp42RunLegacyProbe(t, executable, dir)
			// Re-upgrade after the old binary moved the pruning point itself.
			raw, e := pebble.OpenPebbleDB(dir, 8)
			if e != nil {
				t.Fatal(e)
			}

			node, _, e = consensus.NewFactory().NewConsensus(pp42CrashConfig(), raw, &prefix.Prefix{}, nil)
			if e != nil {
				raw.Close()
				t.Fatal(e)
			}
			pp42MineMove(t, raw, node)
			if found, e := raw.Has(pp42StateKey()); e != nil || !found {
				t.Fatalf("re-upgraded node has no journal marker: %v", e)
			}
			node.Close()
			if e = raw.Close(); e != nil {
				t.Fatal(e)
			}
		})
	}
}

// The --prepare-downgrade entry point (consensus.PrepareDowngrade, which htnd calls
// with its own config and active prefix) runs directly on a database killed inside a
// job, with no prior reopen, then the actual pinned old implementation runs on it.
func TestPP42PrepareDowngradeEntryPoint(t *testing.T) {
	executable := os.Getenv("PP42_BASE_TEST_BINARY")
	if executable == "" {
		t.Skip("set PP42_BASE_TEST_BINARY to the separately built, pinned legacy-consensus probe")
	}
	defer constants.ForceSetBlockVersion(1)
	for _, point := range []string{"preparing", "ready", "data-commit:2", "before-finish"} {
		t.Run(point, func(t *testing.T) {
			dir, _ := pp42CrashAt(t, point)
			raw, e := pebble.OpenPebbleDB(dir, 8)
			if e != nil {
				t.Fatal(e)
			}
			before, e := raw.Get(pp42StateKey())
			if e != nil {
				t.Fatal(e)
			}
			var state pruningstore.JournalState
			if e = json.Unmarshal(before, &state); e != nil || state.Phase == "complete" && point != "before-finish" {
				t.Fatalf("kill at %s left manifest %s: %v", point, before, e)
			}
			report, e := consensus.PrepareDowngrade(context.Background(), pp42CrashConfig(), raw, &prefix.Prefix{})
			if e != nil {
				raw.Close()
				t.Fatal(e)
			}
			t.Logf("report: %s", report)
			if !bytes.Contains([]byte(report), []byte("at phase "+state.Phase)) ||
				!bytes.Contains([]byte(report), []byte("every pruning point UTXO set update is finished")) {
				t.Fatalf("report does not say what was found and done: %s", report)
			}
			if found, e := raw.Has(pp42StateKey()); e != nil || found {
				t.Fatalf("entry point left the journal marker: %v", e)
			}
			pp42RequireNoWorkerGoroutine(t)
			if e = raw.Close(); e != nil {
				t.Fatal(e)
			}
			pp42RunLegacyProbe(t, executable, dir)
		})
	}
}

func TestPP42CurrentMismatchRemainsPending(t *testing.T) {
	cfg := pp42CrashConfig()
	cfg.DeferPruningPointUTXOSetUpdate = false
	defer constants.ForceSetBlockVersion(1)
	h := &scanHold{reached: make(chan struct{}), release: make(chan struct{}), closed: make(chan struct{})}
	var armed atomic.Bool
	n, db, closeNode := newPP42PhaseNode(t, cfg, func(p string) {
		if p == "scan" && armed.CompareAndSwap(true, false) {
			close(h.reached)
			<-h.release
		}
	})
	defer closeNode()
	defer h.Release()
	first := n.insertUntilThePruningPointMoves(n.insertAndUpdate, nil)
	n.waitUntilUpdated()
	armed.Store(true)
	target := n.insertUntilThePruningPointMoves(n.insertAndUpdate, nil)
	n.waitForHold(h)
	extra := n.corruptPruningPointUTXOSet()
	h.Release()
	select {
	case <-n.mismatches:
	case <-time.After(10 * time.Second):
		t.Fatal("current mismatch did not halt")
	}
	if !n.updating() {
		t.Fatal("mismatch cleared pending")
	}
	n.requireRefused(target)
	if n.blockDeleted(first) {
		t.Fatal("mismatch deleted recovery input")
	}
	n.node.Close()
	n.nodeConfig.PruningPointJournalTestHook = nil
	var e error
	// Startup only starts the owner (L3): its check of the corrupt set halts the node
	// instead of failing NewConsensus, and the update stays pending and refused.
	n.node, _, e = consensus.NewFactory().NewConsensus(n.nodeConfig, db, n.dbPrefix, nil)
	if e != nil {
		t.Fatal(e)
	}
	select {
	case <-n.mismatches:
	case <-time.After(10 * time.Second):
		t.Fatal("corrupt restart was accepted")
	}
	if !n.updating() {
		t.Fatal("halted owner cleared pending")
	}
	n.requireRefused(target)
	n.node.Close()
	if e = db.Delete(extra); e != nil {
		t.Fatal(e)
	}
	n.node, _, e = consensus.NewFactory().NewConsensus(n.nodeConfig, db, n.dbPrefix, nil)
	if e != nil {
		t.Fatal(e)
	}
	n.waitUntilUpdated()
	n.requireServedAndVerified(target)
}

func TestPP42PreparingRestartAfterReorg(t *testing.T) {
	defer constants.ForceSetBlockVersion(1)
	cfg := pp42CrashConfig()
	cfg.DeferPruningPointUTXOSetUpdate = false
	h := &scanHold{reached: make(chan struct{}), release: make(chan struct{}), closed: make(chan struct{})}
	var armed atomic.Bool
	n, db, closeNode := newPP42PhaseNode(t, cfg, func(phase string) {
		if phase == "derive-acceptance" && armed.CompareAndSwap(true, false) {
			close(h.reached)
			<-h.release
		}
	})
	defer closeNode()
	defer h.Release()
	n.insertUntilThePruningPointMoves(n.insertAndUpdate, nil)
	n.waitUntilUpdated()
	armed.Store(true)
	target := n.insertUntilThePruningPointMoves(n.insertAndUpdate, nil)
	n.waitForHold(h)
	fork := n.tip
	for i := 0; i < 3; i++ {
		if err := n.insertAndUpdate(n.nextBlock()); err != nil {
			t.Fatal(err)
		}
	}
	oldTip, err := n.node.GetVirtualSelectedParent()
	if err != nil {
		t.Fatal(err)
	}
	branch := fork
	for i := 0; i < 5; i++ {
		branch, _, err = n.source.AddBlock([]*externalapi.DomainHash{branch},
			&externalapi.DomainCoinbaseData{ScriptPublicKey: &externalapi.ScriptPublicKey{Script: []byte{0x51}}, ExtraData: []byte("C1-reorg")}, nil)
		if err != nil {
			t.Fatal(err)
		}
		block, found, err := n.source.GetBlock(branch)
		if err != nil || !found {
			t.Fatal("missing fork block", err)
		}
		if err = n.insertAndUpdate(block); err != nil {
			t.Fatal(err)
		}
	}
	newTip, err := n.node.GetVirtualSelectedParent()
	if err != nil || newTip.Equal(oldTip) || !newTip.Equal(branch) {
		t.Fatal("virtual did not reorganize", err)
	}
	n.tip = branch
	n.requireRefused(target)
	closed := make(chan struct{})
	go func() { n.node.Close(); close(closed) }()
	select {
	case <-closed:
		t.Fatal("owner was not joined")
	case <-time.After(20 * time.Millisecond):
	}
	h.Release()
	select {
	case <-closed:
	case <-time.After(scanTestTimeout):
		t.Fatal("Close did not join")
	}
	n.nodeConfig.PruningPointJournalTestHook = nil

	n.node, _, err = consensus.NewFactory().NewConsensus(n.nodeConfig, db, n.dbPrefix, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 90; i++ {
		if err = n.insertAndUpdate(n.nextBlock()); err != nil {
			t.Fatal(err)
		}
	}
	n.waitUntilUpdated()
	point, err := n.source.PruningPoint()
	if err != nil || !point.Equal(n.pruningPoint()) {
		t.Fatal("different selected target", err)
	}
	want, got := pp42SerializedSet(t, n.source, point), pp42SerializedSet(t, n.node, point)
	if len(got) != len(want) {
		t.Fatal("different set size")
	}
	for i := range want {
		if !bytes.Equal(got[i], want[i]) {
			t.Fatalf("entry %d differs after reorg/restart", i)
		}
	}
	n.requireServedAndVerified(point)
	n.requireNoMismatch()
}
