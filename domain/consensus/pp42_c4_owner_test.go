//go:build pruningtest

package consensus_test

import (
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/testutils"
	"github.com/HoosatNetwork/HTND/v2/domain/prefixmanager/prefix"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database/pebble"
)

// pp42JournalRecords counts the journal's redo records left in the database.
func pp42JournalRecords(t *testing.T, db database.Database, p *prefix.Prefix) int {
	t.Helper()
	c, e := db.Cursor(database.MakeBucket(p.Serialize()).Bucket([]byte("pruning-point-utxo-set-journal")))
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	n := 0
	for ok := c.First(); ok; ok = c.Next() {
		n++
	}
	return n
}

// L1 (UNINTERRUPTED-PP-MOVE §4.1): the owner's synced journal commits run without the
// consensus lock. Each hook below runs right after one: PREPARING (Begin), READY
// (Build) and COMPLETE (Apply). Nothing else runs while the move drains, so a held lock
// can only be the owner's. Also pins the third C1 fix: once the move finished, the
// journal keeps no redo record (the manifest alone stays).
func TestC4SyncedJournalCommitsRunOffLock(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, cfg *consensus.Config) {
		defer constants.ForceSetBlockVersion(1)
		var node atomic.Value
		var mu sync.Mutex
		seen := map[string]bool{}
		n, db, closeNode := newPP42PhaseNode(t, cfg, func(p string) {
			switch p {
			case "preparing", "ready", "complete":
			default:
				return
			}
			c, _ := node.Load().(externalapi.Consensus)
			if c == nil {
				return
			}
			held := consensus.ConsensusLockHeld(c)
			mu.Lock()
			seen[p] = seen[p] || held
			mu.Unlock()
		})
		defer closeNode()
		node.Store(n.node)
		n.insertUntilThePruningPointMoves(n.insertAndUpdate, nil)
		n.waitUntilUpdated()
		second := n.insertUntilThePruningPointMoves(n.insertAndUpdate, nil)
		n.waitUntilUpdated()
		mu.Lock()
		defer mu.Unlock()
		for _, p := range []string{"preparing", "ready", "complete"} {
			held, ok := seen[p]
			if !ok {
				t.Fatalf("the move to %s never reached %s", second, p)
			}
			if held {
				t.Fatalf("the consensus lock was held right after the synced %s commit", p)
			}
		}
		n.pp42RequireSameAsSource(second)
		waitUntil(t, "the finished job's journal records are cleared", func() bool {
			return pp42JournalRecords(t, db, n.dbPrefix) == 0
		})
	})
}

// L3 (UNINTERRUPTED-PP-MOVE §4.1): NewConsensus starts the owner and returns; it never
// runs an unfinished job itself. The reopened node's owner is held inside the apply of
// a job killed at READY; NewConsensus must return meanwhile (before the fix it ran the
// job synchronously and waited on the held hook). Released, the owner finishes the job
// with the old path's exact set and commitment, and the next move matches too.
func TestC4StartupStartsOwnerWithoutRunningJob(t *testing.T) {
	defer constants.ForceSetBlockVersion(1)
	dir, want := pp42CrashAt(t, "ready")
	raw, e := pebble.OpenPebbleDB(dir, 8)
	if e != nil {
		t.Fatal(e)
	}
	held, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	cfg := pp42CrashConfig()
	cfg.PruningPointJournalTestHook = func(p string) {
		if p == "data-commit" {
			once.Do(func() { close(held); <-release })
		}
	}
	type opened struct {
		node externalapi.Consensus
		err  error
	}
	result := make(chan opened, 1)
	go func() {
		node, _, err := consensus.NewFactory().NewConsensus(cfg, raw, &prefix.Prefix{}, nil)
		result <- opened{node, err}
	}()
	var o opened
	select {
	case o = <-result:
	case <-time.After(30 * time.Second):
		close(release)
		t.Fatal("NewConsensus did not return while the owner was held inside the unfinished job")
	}
	if o.err != nil {
		t.Fatal(o.err)
	}
	select {
	case <-held:
	case <-time.After(30 * time.Second):
		t.Fatal("the started owner never resumed the unfinished job")
	}
	close(release)
	pp42WaitDrained(t, raw)
	target, e := externalapi.NewDomainHashFromString(want.Target)
	if e != nil {
		t.Fatal(e)
	}
	var next pp42NextMove
	pp42ReadJSON(t, filepath.Join(dir, "next.json"), &next)
	pp42RequireSet(t, o.node, target, want.Entries, next.Commitment)
	pp42ContinueNextMove(t, raw, o.node, dir)
	o.node.Close()
	pp42RequireNoWorkerGoroutine(t)
	if e = raw.Close(); e != nil {
		t.Fatal(e)
	}
}
