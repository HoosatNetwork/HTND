//go:build pruningtest

package consensus_test

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/processes/pruningmanager"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/v2/domain/prefixmanager/prefix"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database/ldb"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database/pebble"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/logger"
)

// pp42BlockState is a block's history state as a reader sees it.
type pp42BlockState struct {
	Hash             string
	HeaderOnly, Body bool
}

type pp42History struct {
	Blocks     []pp42BlockState
	BlockCount uint64
}

// pp42ChainHistory reads the history state of every block on the selected chain, and
// the block count.
func pp42ChainHistory(t *testing.T, node externalapi.Consensus) pp42History {
	t.Helper()
	chain, e := node.GetVirtualSelectedParentChainFromBlock(pp42CrashConfig().GenesisHash)
	if e != nil {
		t.Fatal(e)
	}
	var h pp42History
	for _, hash := range chain.Added {
		info, e := node.GetBlockInfo(hash)
		if e != nil {
			t.Fatal(e)
		}
		block, found, e := node.GetBlock(hash)
		if e != nil {
			t.Fatal(e)
		}
		h.Blocks = append(h.Blocks, pp42BlockState{hash.String(), info.BlockStatus == externalapi.StatusHeaderOnly,
			found && len(block.Transactions) > 0})
	}
	info, e := node.GetSyncInfo()
	if e != nil {
		t.Fatal(e)
	}
	h.BlockCount = info.BlockCount
	return h
}

// pp42RequireHistory requires the history state the old path's node had right after
// the crash target's move, block by block, and its block count.
func pp42RequireHistory(t *testing.T, node externalapi.Consensus, dir string) {
	t.Helper()
	var want pp42History
	pp42ReadJSON(t, filepath.Join(dir, "history.json"), &want)
	got := map[string]pp42BlockState{}
	for _, b := range pp42ChainHistory(t, node).Blocks {
		got[b.Hash] = b
	}
	for _, b := range want.Blocks {
		if got[b.Hash] != b {
			t.Fatalf("block %s: history state %+v, old path %+v", b.Hash, got[b.Hash], b)
		}
	}
	if info, e := node.GetSyncInfo(); e != nil || info.BlockCount != want.BlockCount {
		t.Fatalf("block count %+v (%v), old path %d", info, e, want.BlockCount)
	}
}

// pp42Counts reads the stored block counts (blocks-count, blocks-count-deleted,
// blocks-deleted) and counts the stored bodies.
func pp42Counts(t *testing.T, db database.DataAccessor, p *prefix.Prefix) (legacy, counted, deleted, bodies uint64) {
	t.Helper()
	root := database.MakeBucket(p.Serialize())
	c, e := db.Cursor(root.Bucket([]byte("blocks")))
	if e != nil {
		t.Fatal(e)
	}
	for ok := c.First(); ok; ok = c.Next() {
		bodies++
	}
	c.Close()
	count := func(name string) uint64 {
		v, e := db.Get(root.Key([]byte(name)))
		if database.IsNotFoundError(e) {
			return 0
		}
		if e != nil {
			t.Fatal(e)
		}
		if len(v) < 2 {
			return 0
		}
		// DbBlockCount: field 1 varint.
		n, shift := uint64(0), uint(0)
		for _, b := range v[1:] {
			n |= uint64(b&0x7f) << shift
			shift += 7
			if b < 0x80 {
				break
			}
		}
		return n
	}
	return count("blocks-count"), count("blocks-count-deleted"), count("blocks-deleted"), bodies
}

// pp42StoredCount is the count startup takes from the stored keys without a recount
// (PREP B7): blocks-count minus the deletions after the last block commit.
func pp42StoredCount(t *testing.T, db database.DataAccessor, p *prefix.Prefix) uint64 {
	t.Helper()
	legacy, counted, deleted, _ := pp42Counts(t, db, p)
	if counted > deleted || deleted-counted > legacy {
		t.Fatalf("blocks-count %d, blocks-count-deleted %d, blocks-deleted %d contradict each other", legacy, counted,
			deleted)
	}
	return legacy - (deleted - counted)
}

// pp42RequireCountMatchesBodies requires the stored keys to give the stored bodies (H14
// with single-writer counters, c′, PREP B7). The legacy blocks-count is written by
// foreground block commits only, so after history deletion batches it may be high until
// the next one, by the bodies deleted since: blocks-deleted - blocks-count-deleted.
func pp42RequireCountMatchesBodies(t *testing.T, db database.DataAccessor, p *prefix.Prefix) {
	t.Helper()
	legacy, counted, deleted, bodies := pp42Counts(t, db, p)
	if got := pp42StoredCount(t, db, p); got != bodies {
		t.Fatalf("blocks-count %d - (blocks-deleted %d - blocks-count-deleted %d) = %d, stored bodies %d", legacy,
			deleted, counted, got, bodies)
	}
}

// pp42RequireCountExact requires node's Count() (its sync info's block count) and the
// stored keys to equal the stored bodies (PREP B7).
func pp42RequireCountExact(t *testing.T, node externalapi.Consensus, db database.DataAccessor, p *prefix.Prefix) {
	t.Helper()
	pp42RequireCountMatchesBodies(t, db, p)
	_, _, _, bodies := pp42Counts(t, db, p)
	if info, e := node.GetSyncInfo(); e != nil || info.BlockCount != bodies {
		t.Fatalf("Count() %+v (%v), stored bodies %d", info, e, bodies)
	}
}

// pp42RequireLegacyCountExact requires the legacy blocks-count to equal the stored bodies,
// as startup and a downgrade normalization leave it, with no deletion after it
// (blocks-count-deleted = blocks-deleted, or both absent).
func pp42RequireLegacyCountExact(t *testing.T, db database.DataAccessor, p *prefix.Prefix) {
	t.Helper()
	legacy, counted, deleted, bodies := pp42Counts(t, db, p)
	if legacy != bodies || counted != deleted {
		t.Fatalf("blocks-count %d, blocks-count-deleted %d, blocks-deleted %d, stored bodies %d", legacy, counted,
			deleted, bodies)
	}
}

// pp42Dump reads every key under the prefix, except the keys that differ by design
// between the old single-commit deletion and the job: the job records, the split
// counters, the legacy blocks-count (refreshed by the next block commit after a job,
// pp42RequireCountMatchesBodies), the wall-clock last pruning time and the C1 journal.
func pp42Dump(t *testing.T, db database.DataAccessor, p *prefix.Prefix) map[string][]byte {
	t.Helper()
	c, e := db.Cursor(database.MakeBucket(p.Serialize()))
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	out := map[string][]byte{}
	for ok := c.First(); ok; ok = c.Next() {
		k, e := c.Key()
		if e != nil {
			t.Fatal(e)
		}
		key := string(k.Bytes())
		if strings.Contains(key, "history-deletion-jobs/") || strings.HasSuffix(key, "blocks-count-deleted") || strings.HasSuffix(key, "blocks-count") ||
			strings.HasSuffix(key, "blocks-deleted") || strings.HasSuffix(key, "last-pruning-time") ||
			strings.Contains(key, "pruning-point-utxo-set-journal") {
			continue
		}
		v, e := c.Value()
		if e != nil {
			t.Fatal(e)
		}
		out[key] = bytes.Clone(v)
	}
	return out
}

func pp42RequireSameDump(t *testing.T, what string, got, want map[string][]byte) {
	t.Helper()
	var diff []string
	for k, v := range want {
		if g, ok := got[k]; !ok || !bytes.Equal(g, v) {
			diff = append(diff, fmt.Sprintf("%q present=%t", k, ok))
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			diff = append(diff, fmt.Sprintf("%q extra", k))
		}
	}
	if len(diff) > 0 {
		sort.Strings(diff)
		t.Fatalf("%s: %d keys differ from the old single-commit deletion, first: %s", what, len(diff),
			strings.Join(diff[:min(5, len(diff))], "; "))
	}
}

// pp42Twin is another node fed the same blocks as a pruningMoveNode.
type pp42Twin struct {
	db   database.Database
	node externalapi.Consensus
}

func newPP42Twin(t *testing.T, n *pruningMoveNode, db database.Database, deferUpdate, archival bool,
	hook func(string)) *pp42Twin {
	t.Helper()
	cfg := *n.nodeConfig
	cfg.DeferPruningPointUTXOSetUpdate = deferUpdate
	cfg.IsArchival = archival
	cfg.PruningPointJournalTestHook = hook
	node, _, e := consensus.NewFactory().NewConsensus(&cfg, db, n.dbPrefix, nil)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { node.Close(); db.Close() })
	return &pp42Twin{db, node}
}

func pp42OpenPebble(t *testing.T) database.Database {
	t.Helper()
	db, e := pebble.OpenPebbleDB(t.TempDir(), 8)
	if e != nil {
		t.Fatal(e)
	}
	return db
}

func (tw *pp42Twin) drained(t *testing.T, n *pruningMoveNode) bool {
	updating, e := tw.db.Has(n.updatingKey)
	if e != nil {
		t.Fatal(e)
	}
	return !updating && !historyDeletionPending(t, tw.db, n.dbPrefix)
}

// TestC4HistoryEndStateEqualsLegacy is the end-state oracle for history deletion (P07
// T1, T3, T9, T20; UNINTERRUPTED-PP-MOVE D2, D4 for a deletion job, D10, A1, A2, A3, A4).
// Twin nodes take the same blocks:
//   - legacy: its "history-recorded" hook deletes each job's history with base c7d5c3f3e's
//     downward walk in one staging commit, the deletion the finish used to do;
//   - owner:  the c4 node, keep set and walk on a snapshot, batches of 3 committed
//     off-lock behind the published mask (c′);
//   - sync:   without deferred updates, the job runs under the finish's lock hold;
//   - ldb:    deferred updates on LevelDB, which has no snapshot: the synchronous path.
//
// After each move has drained, every database must equal the legacy one key for key and
// byte for byte, except the keys that differ by design (pp42Dump). With overlap, the
// owner is held after planning the second move's job until the third move has finished
// and recorded its own job: two records, run in index order.
func TestC4HistoryEndStateEqualsLegacy(t *testing.T) {
	defer func(k int) { pruningmanager.HistoryDeletionBatchBlocks = k }(pruningmanager.HistoryDeletionBatchBlocks)
	pruningmanager.HistoryDeletionBatchBlocks = 3
	plans := map[bool][]int{}
	for _, archival := range []bool{false, true} {
		for _, overlap := range []bool{false, true} {
			t.Run(fmt.Sprintf("archival=%t/overlap=%t", archival, overlap), func(t *testing.T) {
				defer constants.ForceSetBlockVersion(1)
				cfg := pp42CrashConfig()
				cfg.IsArchival = archival
				var owner atomic.Value
				var hold atomic.Bool
				held, release := make(chan struct{}), make(chan struct{})
				var plan []int
				n, db, closeNode := newPP42PhaseNode(t, cfg, func(p string) {
					if p != "history-planned" {
						return
					}
					if node, ok := owner.Load().(externalapi.Consensus); ok && !overlap {
						planned, _, _, e := consensus.HistoryDeletionPlanForTest(node)
						if e != nil {
							panic(e)
						}
						plan = append(plan, len(planned))
					}
					if hold.CompareAndSwap(true, false) {
						close(held)
						<-release
					}
				})
				defer closeNode()
				owner.Store(n.node)
				var legacyNode atomic.Value
				legacy := newPP42Twin(t, n, pp42OpenPebble(t), true, archival, func(p string) {
					if p == "history-recorded" {
						if node, ok := legacyNode.Load().(externalapi.Consensus); ok {
							if e := consensus.RunLegacyHistoryDeletionForTest(node); e != nil {
								panic(e)
							}
						}
					}
				})
				legacyNode.Store(legacy.node)
				sync := newPP42Twin(t, n, pp42OpenPebble(t), false, archival, nil)
				ldbDB, e := ldb.NewLevelDB(t.TempDir(), 8)
				if e != nil {
					t.Fatal(e)
				}
				onLevelDB := newPP42Twin(t, n, ldbDB, true, archival, nil)
				var pruned *pp42Twin
				if archival {
					pruned = newPP42Twin(t, n, pp42OpenPebble(t), true, false, nil)
				}
				twins := []*pp42Twin{legacy, sync, onLevelDB}
				if pruned != nil {
					twins = append(twins, pruned)
				}
				insert := func(b *externalapi.DomainBlock) error {
					if e := n.insertAndUpdate(b); e != nil {
						return e
					}
					for _, tw := range twins {
						if e := tw.node.ValidateAndInsertBlock(b, true, true); e != nil {
							return e
						}
						if e := tw.node.UpdatePruningPointIfRequired(); e != nil {
							return e
						}
					}
					return nil
				}
				drained := func() bool {
					for _, tw := range twins {
						if !tw.drained(t, n) {
							return false
						}
					}
					return !n.updating() && !historyDeletionPending(t, n.db, n.dbPrefix)
				}
				for move := 0; move < 4; move++ {
					if overlap && move == 2 {
						hold.Store(true)
					}
					point := n.insertUntilThePruningPointMoves(insert, nil)
					if overlap && move == 2 {
						select {
						case <-held:
						case <-time.After(scanTestTimeout):
							t.Fatal("the owner never planned the job")
						}
						point = n.insertUntilThePruningPointMoves(insert, nil)
						move++
						waitUntil(t, "the next move recorded its job behind the held one", func() bool {
							jobs := 0
							c, e := db.Cursor(database.MakeBucket(n.dbPrefix.Serialize()).Bucket([]byte("history-deletion-jobs")))
							if e != nil {
								t.Fatal(e)
							}
							for ok := c.First(); ok; ok = c.Next() {
								jobs++
							}
							c.Close()
							return jobs == 2 && !n.updating()
						})
						close(release)
					}
					waitUntil(t, "every node drained the move", drained)
					want := pp42Dump(t, legacy.db, n.dbPrefix)
					pp42RequireSameDump(t, fmt.Sprintf("move %d owner", move), pp42Dump(t, db, n.dbPrefix), want)
					pp42RequireSameDump(t, fmt.Sprintf("move %d sync", move), pp42Dump(t, sync.db, n.dbPrefix), want)
					pp42RequireSameDump(t, fmt.Sprintf("move %d leveldb", move), pp42Dump(t, onLevelDB.db, n.dbPrefix), want)
					pp42RequireCountMatchesBodies(t, db, n.dbPrefix)
					pp42RequireCountMatchesBodies(t, sync.db, n.dbPrefix)
					pp42RequireCountMatchesBodies(t, onLevelDB.db, n.dbPrefix)
					// The count startup takes equals the old path's blocks-count.
					legacyCount, _, _, _ := pp42Counts(t, legacy.db, n.dbPrefix)
					if got := pp42StoredCount(t, db, n.dbPrefix); got != legacyCount {
						t.Fatalf("move %d: stored count %d, old path blocks-count %d", move, got, legacyCount)
					}
					// A2: no PP-owned record outlives the move: no job, no journal record.
					if records := pp42JournalRecords(t, db, n.dbPrefix); records != 0 {
						t.Fatalf("move %d: %d journal records left", move, records)
					}
					// The dumps include the served bucket: the sync twin wrote it with the
					// legacy writer, so the owner's journal-written set is byte-identical to it.
					n.requireServedAndVerified(point)
					if pruned != nil {
						// A4: the archival node serves the pruned node's exact set.
						a, b := pp42SerializedSet(t, n.node, point), pp42SerializedSet(t, pruned.node, point)
						if len(a) != len(b) {
							t.Fatalf("archival serves %d entries, pruned %d", len(a), len(b))
						}
						for i := range a {
							if !bytes.Equal(a[i], b[i]) {
								t.Fatalf("archival served entry %d differs from the pruned node's", i)
							}
						}
					}
				}
				headerOnly, withBody := pp42PastBodies(t, n.node)
				if headerOnly == 0 || (archival && withBody != headerOnly) || (!archival && withBody != 0) {
					t.Fatalf("below the pruning point: %d header-only blocks, %d with a body", headerOnly, withBody)
				}
				// A3: the archival walk plans exactly what the pruned walk plans on the same blocks.
				if !overlap {
					plans[archival] = plan
					if archival && fmt.Sprint(plans[true]) != fmt.Sprint(plans[false]) {
						t.Fatalf("archival plans %v, pruned plans %v", plans[true], plans[false])
					}
					t.Logf("planned blocks per job: %v", plan)
				}
			})
		}
	}
}

// TestC4HistoryBatchesArePastClosed checks the bottom-up order and the reader side of
// every batch (P07 T5, T11, T12, T13; UNINTERRUPTED-PP-MOVE D5, D6, D12, A1). Before
// the first batch the test reads the acceptance data of every planned block, so the
// caches hold entries for blocks about to be deleted. After each batch (its off-lock
// commit and its off-lock eviction done):
//   - a new walk of the job on a snapshot (what is stored, not the mask) finds exactly
//     the plan minus the deleted prefix (past-closure, resumption = re-walk);
//   - locked readers see the deleted blocks as deleted: acceptance data not found, no
//     error of any other kind (archival: still found), header-only status; the lock-free
//     block read agrees; nothing stale comes back from a cache;
//   - a locked anticone read, a block insert and a block template complete in time.
//
// The windows between publication, commit and eviction (D12′) are TestC4HistoryMaskWindows.
func TestC4HistoryBatchesArePastClosed(t *testing.T) {
	defer func(k int) { pruningmanager.HistoryDeletionBatchBlocks = k }(pruningmanager.HistoryDeletionBatchBlocks)
	pruningmanager.HistoryDeletionBatchBlocks = 3
	for _, archival := range []bool{false, true} {
		t.Run(fmt.Sprintf("archival=%t", archival), func(t *testing.T) {
			defer constants.ForceSetBlockVersion(1)
			cfg := pp42CrashConfig()
			cfg.IsArchival = archival
			var node atomic.Value
			var armed atomic.Bool
			var plan []*externalapi.DomainHash
			var batches int
			var failure atomic.Value
			fail := func(format string, args ...interface{}) { failure.CompareAndSwap(nil, fmt.Sprintf(format, args...)) }
			var n *pruningMoveNode
			check := func(p string) {
				c, ok := node.Load().(externalapi.Consensus)
				if !ok || !armed.Load() {
					return
				}
				switch p {
				case "history-planned":
					planned, _, found, e := consensus.HistoryDeletionPlanForTest(c)
					if e != nil || !found {
						fail("plan: %v %t", e, found)
						return
					}
					plan, batches = planned, 0
					for _, h := range plan {
						if _, e := c.GetBlockAcceptanceData(h); e != nil {
							fail("acceptance data of planned block %s: %v", h, e)
						}
					}
					// D6: the foreground runs while the owner is between its plan and its batches.
					if e := n.insertAndUpdate(n.nextBlock()); e != nil {
						fail("insert after the plan: %v", e)
					}
				case "history-batch":
					batches++
					done := min(batches*pruningmanager.HistoryDeletionBatchBlocks, len(plan))
					remainder, _, found, e := consensus.HistoryDeletionPlanForTest(c)
					if e != nil {
						fail("re-walk: %v", e)
						return
					}
					if found != (done < len(plan)) || fmt.Sprint(remainder) != fmt.Sprint(plan[done:]) {
						fail("after batch %d the re-walk finds %d blocks (record %t), want the %d planned after %d",
							batches, len(remainder), found, len(plan)-done, done)
					}
					for _, h := range plan[min((batches-1)*pruningmanager.HistoryDeletionBatchBlocks, len(plan)):done] {
						info, e := c.GetBlockInfo(h)
						if e != nil || info.BlockStatus != externalapi.StatusHeaderOnly {
							fail("deleted block %s: %+v %v", h, info, e)
						}
						_, e = c.GetBlockAcceptanceData(h)
						if archival && e != nil {
							fail("archival acceptance data of %s: %v", h, e)
						}
						if !archival && !errors.Is(e, database.ErrNotFound) {
							fail("acceptance data of deleted block %s: got %v, want not found", h, e)
						}
						block, found, e := c.GetBlock(h)
						if bodyFound := found && len(block.Transactions) > 0; e != nil || bodyFound != archival {
							fail("lock-free read of deleted block %s: found %t, %v", h, bodyFound, e)
						}
					}
					if _, e := c.GetAnticone(n.pruningPoint(), n.tip, 1<<20); e != nil {
						fail("anticone: %v", e)
					}
					if e := n.insertAndUpdate(n.nextBlock()); e != nil {
						fail("insert between batches: %v", e)
					}
					if _, e := c.BuildBlockTemplate(&externalapi.DomainCoinbaseData{
						ScriptPublicKey: &externalapi.ScriptPublicKey{Script: []byte{0x51}}}, nil); e != nil {
						fail("template between batches: %v", e)
					}
				}
			}
			var closeNode func()
			n, _, closeNode = newPP42PhaseNode(t, cfg, check)
			defer closeNode()
			node.Store(n.node)
			n.insertUntilThePruningPointMoves(n.insertAndUpdate, nil)
			n.waitUntilUpdated()
			armed.Store(true)
			n.insertUntilThePruningPointMoves(n.insertAndUpdate, nil)
			n.waitUntilUpdated()
			armed.Store(false)
			if f := failure.Load(); f != nil {
				t.Fatal(f)
			}
			if len(plan) < 2*pruningmanager.HistoryDeletionBatchBlocks || batches < 2 {
				t.Fatalf("fixture too small: %d planned blocks in %d batches", len(plan), batches)
			}
			t.Logf("%d planned blocks deleted in %d batches", len(plan), batches)
		})
	}
}

// TestC4CrashMatrix kills a whole node at every C1 journal boundary and at every
// history deletion boundary, reopens it through the factory, and requires the old
// path's set and commitment, its exact history state block by block, its block count,
// and then the next move (UNINTERRUPTED-PP-MOVE D1, D3, A2; P07 T6, T7, T8, T20; gate C
// of panel-42 SYNTHESIS for c′). The history points: after the finish commit
// (history-recorded), after the plan, after the mask is published before batch 1, before
// a batch commit, after a batch commit before its eviction for the first and second
// batch, after the second batch, and after the last commit before the mask is removed.
// Batches hold two blocks. A resumed job must publish exactly its remainder as the mask
// before its first batch commits; after the last reopen the legacy blocks-count is exact.
// archival=true is the archival kill matrix (A2) at the same points: a status-only mask.
// The pruned node also takes the two D1 disk-fallback points (pp42FallbackCrashPoints).
func TestC4CrashMatrix(t *testing.T) {
	defer constants.ForceSetBlockVersion(1)
	points := append(append([]string(nil), pp42CrashPoints...), "history-recorded", "history-planned",
		"history-mask-published", "history-before-batch-commit", "history-batch-commit", "history-batch-commit:2",
		"history-batch:2", "history-last-batch-commit")
	for _, archival := range []bool{false, true} {
		armPoints := points
		if !archival {
			armPoints = append(append([]string(nil), points...), pp42FallbackCrashPoints...)
		}
		for _, point := range armPoints {
			t.Run(fmt.Sprintf("archival=%t/%s", archival, point), func(t *testing.T) {
				t.Setenv("PP42_ARCHIVAL", map[bool]string{true: "1", false: "0"}[archival])
				dir, want := pp42CrashAt(t, point)
				var reopened atomic.Value
				var published atomic.Bool
				var failure atomic.Value
				fail := func(format string, args ...interface{}) { failure.CompareAndSwap(nil, fmt.Sprintf(format, args...)) }
				hook := func(p string) {
					switch p {
					case "history-before-batch-commit", "history-batch-commit":
						if !published.Load() {
							fail("the resumed job reached %s before publishing its mask", p)
						}
					case "history-mask-published":
						deadline := time.Now().Add(30 * time.Second)
						for reopened.Load() == nil && time.Now().Before(deadline) {
							time.Sleep(time.Millisecond)
						}
						c, ok := reopened.Load().(externalapi.Consensus)
						if !ok {
							fail("the reopened node never returned")
							return
						}
						masked, withData := consensus.HistoryMaskBlocksForTest(c)
						remainder, _, found, e := consensus.HistoryDeletionPlanForTest(c)
						if e != nil || !found {
							fail("remainder: %v %t", e, found)
							return
						}
						sort.Slice(remainder, func(i, j int) bool { return remainder[i].Less(remainder[j]) })
						if fmt.Sprint(masked) != fmt.Sprint(remainder) || withData == archival {
							fail("the resumed job published %d blocks (data %t), the remainder is %d", len(masked),
								withData, len(remainder))
						}
						published.Store(true)
					}
				}
				hadJob := pp42HistoryJobRecorded(t, dir)
				raw, node := pp42OpenRecoveredWithHook(t, dir, want, hook,
					func(c externalapi.Consensus) { reopened.Store(c) })
				if f := failure.Load(); f != nil {
					t.Fatal(f)
				}
				if hadJob && !published.Load() {
					t.Fatal("the resumed job deleted without publishing a mask")
				}
				pp42RequireHistory(t, node, dir)
				// PREP B7, after a crash: Count() exact.
				pp42RequireCountExact(t, node, raw, &prefix.Prefix{})
				pp42ContinueNextMove(t, raw, node, dir)
				pp42RequireCountExact(t, node, raw, &prefix.Prefix{})
				headerOnly, withBody := pp42PastBodies(t, node)
				if headerOnly == 0 || (archival && withBody != headerOnly) || (!archival && withBody != 0) {
					t.Fatalf("below the pruning point: %d header-only blocks, %d with a body", headerOnly, withBody)
				}
				node.Close()
				pp42RequireNoWorkerGoroutine(t)
				legacyCount, counted, deleted, bodies := pp42Counts(t, raw, &prefix.Prefix{})
				t.Logf("at the clean stop: blocks-count %d, blocks-count-deleted %d, blocks-deleted %d, stored bodies %d",
					legacyCount, counted, deleted, bodies)
				// Startup rewrites a legacy blocks-count the batches left behind; PREP B7, after a
				// normal restart: Count() exact.
				node, _, e := consensus.NewFactory().NewConsensus(pp42CrashConfig(), raw, &prefix.Prefix{}, nil)
				if e != nil {
					t.Fatal(e)
				}
				pp42RequireLegacyCountExact(t, raw, &prefix.Prefix{})
				pp42RequireCountExact(t, node, raw, &prefix.Prefix{})
				node.Close()
				if e := raw.Close(); e != nil {
					t.Fatal(e)
				}
			})
		}
	}
}

// pp42HistoryJobRecorded reports whether the killed database in dir holds a history
// deletion job record.
func pp42HistoryJobRecorded(t *testing.T, dir string) bool {
	t.Helper()
	raw, e := pebble.OpenPebbleDB(dir, 8)
	if e != nil {
		t.Fatal(e)
	}
	defer raw.Close()
	return historyDeletionPending(t, raw, &prefix.Prefix{})
}

// TestC4HistoryBatchFailureRetries injects disk-full into history deletion batch
// commits after the first batch (UNINTERRUPTED-PP-MOVE D8 batch half, P07 T16; on
// archival the coordinator's archival form: a status-only batch frees nothing and
// still must not stop the node). Three commits fail. Required: each failed batch
// leaves nothing partial (the job record and the finished batch stay), the owner
// retries with the doubling backoff, blocks and templates keep working while it
// waits, and the job then completes to the old path's history state with an exact
// block count.
func TestC4HistoryBatchFailureRetries(t *testing.T) {
	defer func(k int) { pruningmanager.HistoryDeletionBatchBlocks = k }(pruningmanager.HistoryDeletionBatchBlocks)
	pruningmanager.HistoryDeletionBatchBlocks = 3
	defer consensus.SetHistoryDeletionRetryForTest(50*time.Millisecond, 200*time.Millisecond)()
	for _, archival := range []bool{false, true} {
		t.Run(fmt.Sprintf("archival=%t", archival), func(t *testing.T) {
			defer constants.ForceSetBlockVersion(1)
			cfg := pp42CrashConfig()
			cfg.IsArchival = archival
			cfg.DeferPruningPointUTXOSetUpdate = false // the source finishes each move at once: the reference
			var armed atomic.Bool
			var db *pp42ViewDB
			var faultedAt atomic.Int64
			n, viewDB, closeNode := newPP42PhaseNode(t, cfg, func(p string) {
				if p == "history-batch" && armed.CompareAndSwap(true, false) {
					db.failHistoryCommits.Store(3)
					faultedAt.Store(time.Now().UnixNano())
				}
			})
			db = viewDB
			defer closeNode()
			n.insertUntilThePruningPointMoves(n.insertAndUpdate, nil)
			n.waitUntilUpdated()
			armed.Store(true)
			n.insertUntilThePruningPointMoves(n.insertAndUpdate, nil)
			waitUntil(t, "the first injected failure", func() bool { return db.failed.Load() >= 1 })
			if !historyDeletionPending(t, db, n.dbPrefix) {
				t.Fatal("a failed batch removed the job record")
			}
			for i := 0; i < 10; i++ {
				if e := n.insertAndUpdate(n.nextBlock()); e != nil {
					t.Fatal(e)
				}
			}
			if e := n.within("template while the job waits", func() error {
				_, e := n.node.BuildBlockTemplate(&externalapi.DomainCoinbaseData{
					ScriptPublicKey: &externalapi.ScriptPublicKey{Script: []byte{0x51}}}, nil)
				return e
			}); e != nil {
				t.Fatal(e)
			}
			n.waitUntilUpdated()
			elapsed := time.Since(time.Unix(0, faultedAt.Load()))
			if f := db.failed.Load(); f != 3 {
				t.Fatalf("%d injected failures, want 3", f)
			}
			// Backoff 50 + 100 + 200 ms between the four attempts.
			if elapsed < 300*time.Millisecond {
				t.Fatalf("the job finished %s after the first failure: no backoff", elapsed)
			}
			n.requireNoMismatch()
			pp42RequireCountMatchesBodies(t, db, n.dbPrefix)
			want := pp42ChainHistory(t, n.source)
			got := pp42ChainHistory(t, n.node)
			if fmt.Sprint(got) != fmt.Sprint(want) {
				t.Fatalf("history after the retries differs from the old path:\n got %v\nwant %v", got, want)
			}
		})
	}
}

// pp42LogCapture appends the global log backend's lines to a file.
type pp42LogCapture struct {
	mu sync.Mutex
	w  *bufio.Writer
	f  *os.File
}

func (c *pp42LogCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.w == nil {
		return len(p), nil
	}
	return c.w.Write(p)
}

func (c *pp42LogCapture) Close() error { return nil }

func (c *pp42LogCapture) stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.w.Flush()
	c.f.Close()
	c.w = nil
}

// TestC4LockAccounting runs two full moves on an owner node with the lab log on and
// checks the lock accounting of every pruning point lock hold (UNINTERRUPTED-PP-MOVE
// D7, L2): each owner hold is a [PP42] phase=lock line with a known section and the
// move's pruning point as target; every [PP-TIMING] lock line is ownership=held or an
// off-lock not-held check; none of the node's own manager is unknown. History holds are one deletion-snapshot,
// one deletion-publish (the mask) and one deletion-retire (mask removed after the last eviction) per job, and
// none per batch (c′: batches commit and evict off-lock).
// The P01 phase lines are written after the unlock, so the level stays on 200 ms past the last move. The lines are written to $C4_D7_LOG when
// set, for the runner's parser. No hold duration is asserted: none is measurable here.
func TestC4LockAccounting(t *testing.T) {
	if logger.BackendLog.IsRunning() {
		t.Skip("the global log backend already runs in this process; run this test on its own")
	}
	path := os.Getenv("C4_D7_LOG")
	if path == "" {
		path = filepath.Join(t.TempDir(), "d7.log")
	}
	f, e := os.Create(path)
	if e != nil {
		t.Fatal(e)
	}
	capture := &pp42LogCapture{w: bufio.NewWriter(f), f: f}
	if e = logger.BackendLog.AddLogWriter(capture, logger.LevelDebug); e != nil {
		t.Fatal(e)
	}
	if e = logger.BackendLog.Run(); e != nil {
		t.Fatal(e)
	}
	defer func(k int) { pruningmanager.HistoryDeletionBatchBlocks = k }(pruningmanager.HistoryDeletionBatchBlocks)
	pruningmanager.HistoryDeletionBatchBlocks = 3
	defer constants.ForceSetBlockVersion(1)
	var jobs, batches atomic.Int32
	n, _, closeNode := newPP42PhaseNode(t, pp42CrashConfig(), func(p string) {
		switch p {
		case "history-planned":
			jobs.Add(1)
		case "history-batch":
			batches.Add(1)
		}
	})
	defer closeNode()
	n.insertUntilThePruningPointMoves(n.insertAndUpdate, nil)
	n.waitUntilUpdated()
	prnm := logger.RegisterSubSystem("PRNM")
	prnm.SetLevel(logger.LevelDebug)
	defer prnm.SetLevel(logger.LevelOff)
	jobs.Store(0)
	batches.Store(0)
	var points []string
	for i := 0; i < 2; i++ {
		points = append(points, n.insertUntilThePruningPointMoves(n.insertAndUpdate, nil).String())
		n.waitUntilUpdated()
	}
	time.Sleep(200 * time.Millisecond)
	prnm.SetLevel(logger.LevelOff)
	capture.stop()
	data, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	worker := regexp.MustCompile(`\[PP42\] phase=lock target=(\S+) section=(\S+) lock_wait_ns=\d+ lock_hold_ns=(\d+) start_ns=(\d+) end_ns=(\d+)$`)
	timing := regexp.MustCompile(`\[PP-TIMING\] kind=lock instance=(\S+) .* caller=(\S+) ownership=(\S+) `)
	instance := consensus.PruningManagerInstanceForTest(n.node)
	maskRemoved := regexp.MustCompile(`\[PP42\] phase=deletion-mask-removed index=\d+ masked_reads_under_lock status=\d+ `)
	sections, maskLines := map[string]int{}, 0
	for _, line := range strings.Split(string(data), "\n") {
		if maskRemoved.MatchString(line) {
			maskLines++
		}
		if m := worker.FindStringSubmatch(line); m != nil {
			sections[m[2]]++
			// PREP B3: monotonic start/end on P01's epoch span exactly the logged hold.
			hold, _ := strconv.ParseInt(m[3], 10, 64)
			start, _ := strconv.ParseInt(m[4], 10, 64)
			end, _ := strconv.ParseInt(m[5], 10, 64)
			if start <= 0 || end-start != hold {
				t.Fatalf("lock line's start_ns/end_ns do not span its hold: %q", line)
			}
			switch m[2] {
			case "capture", "admit", "scan", "deletion-snapshot", "deletion-publish", "deletion-retire":
				if !strings.Contains(strings.Join(points, " "), m[1]) {
					t.Fatalf("hold with section %s names target %s, not a move of this run %v", m[2], m[1], points)
				}
			case "idle":
			default:
				t.Fatalf("unknown owner lock section in %q", line)
			}
		}
		// Only the node's own manager: the old-path source and the owners' private read views
		// (off-lock by construction) log under other instances.
		if m := timing.FindStringSubmatch(line); m != nil && m[1] == instance && m[3] != "held" &&
			!(m[3] == "not-held" && m[2] == "scan-check") {
			t.Fatalf("unattributed pruning point lock line: %q", line)
		}
	}
	t.Logf("owner lock holds by section over two moves: %v; jobs %d, batches %d, masked-read lines %d; log %s",
		sections, jobs.Load(), batches.Load(), maskLines, path)
	if sections["deletion-snapshot"] != int(jobs.Load()) || sections["deletion-publish"] != int(jobs.Load()) ||
		sections["deletion-retire"] != int(jobs.Load()) || sections["deletion-evict"] != 0 || sections["deletion-batch"] != 0 ||
		jobs.Load() != 2 || maskLines != 2 || sections["capture"] == 0 || sections["scan"] == 0 {
		t.Fatalf("owner holds %v do not match %d jobs and %d batches", sections, jobs.Load(), batches.Load())
	}
}

// TestC4HistoryDowngrade runs the pinned old implementation (PP42_BASE_TEST_BINARY,
// LEGACY-PROBE.md) on a database killed inside a history deletion job
// (UNINTERRUPTED-PP-MOVE D9 for the deletion phases, P07 T18, T19). With normalize,
// --prepare-downgrade's PrepareDowngrade runs first: it must finish the job, leave no
// record, an exact blocks-count and no split counter. Without it the old binary opens the mid-job
// database directly: the UTXO update is finished there, so it must serve the exact
// set, mine, move (its own walk deletes the remainder) and reopen, ignoring the
// unknown keys. The new binary then reopens (re-upgrade) and completes a move with an
// exact block count. Without normalization it first re-initialises its journal from
// the old binary's finished set (PREP B2) and serves exactly that set.
func TestC4HistoryDowngrade(t *testing.T) {
	executable := os.Getenv("PP42_BASE_TEST_BINARY")
	if executable == "" {
		t.Skip("set PP42_BASE_TEST_BINARY to the separately built, pinned legacy-consensus probe")
	}
	defer constants.ForceSetBlockVersion(1)
	for _, normalize := range []bool{true, false} {
		for _, point := range []string{"history-recorded", "history-batch-commit:2"} {
			t.Run(fmt.Sprintf("normalize=%t/%s", normalize, point), func(t *testing.T) {
				dir, _ := pp42CrashAt(t, point)
				raw, e := pebble.OpenPebbleDB(dir, 8)
				if e != nil {
					t.Fatal(e)
				}
				if !historyDeletionPending(t, raw, &prefix.Prefix{}) {
					t.Fatalf("kill at %s left no history deletion job", point)
				}
				if normalize {
					report, e := consensus.PrepareDowngrade(context.Background(), pp42CrashConfig(), raw, &prefix.Prefix{})
					if e != nil {
						raw.Close()
						t.Fatal(e)
					}
					t.Logf("report: %s", report)
					if historyDeletionPending(t, raw, &prefix.Prefix{}) {
						t.Fatal("PrepareDowngrade left a history deletion job")
					}
					// Normalized: the exact legacy blocks-count, and no split counter the old
					// binary would leave stale.
					pp42RequireLegacyCountExact(t, raw, &prefix.Prefix{})
					if _, counted, deleted, _ := pp42Counts(t, raw, &prefix.Prefix{}); counted != 0 || deleted != 0 {
						t.Fatalf("PrepareDowngrade left blocks-count-deleted %d, blocks-deleted %d", counted, deleted)
					}
					pp42RequireNoWorkerGoroutine(t)
				}
				lagLegacy, lagCounted, lagDeleted, lagBodies := pp42Counts(t, raw, &prefix.Prefix{})
				t.Logf("handed to the old binary: blocks-count %d, blocks-count-deleted %d, blocks-deleted %d, "+
					"stored bodies %d", lagLegacy, lagCounted, lagDeleted, lagBodies)
				if e = raw.Close(); e != nil {
					t.Fatal(e)
				}
				pp42RunLegacyProbe(t, executable, dir)
				raw, e = pebble.OpenPebbleDB(dir, 8)
				if e != nil {
					t.Fatal(e)
				}
				legacy := pp42LegacyBucketEntries(t, raw)
				// Without normalization the old binary moved the pruning point past the
				// journal manifest: the re-upgrade re-initialises it (PREP B2) and starts.
				node, _, e := consensus.NewFactory().NewConsensus(pp42CrashConfig(), raw, &prefix.Prefix{}, nil)
				if e != nil {
					raw.Close()
					t.Fatal(e)
				}
				pp42WaitDrained(t, raw)
				legacyCount, counted, deleted, bodies := pp42Counts(t, raw, &prefix.Prefix{})
				t.Logf("after the re-upgrade: blocks-count %d, blocks-count-deleted %d, blocks-deleted %d, stored bodies %d",
					legacyCount, counted, deleted, bodies)
				// PREP B7: exact after the old binary ran, normalized or not.
				pp42RequireCountExact(t, node, raw, &prefix.Prefix{})
				if !normalize {
					point, e := node.PruningPoint()
					if e != nil {
						t.Fatal(e)
					}
					if served := pp42SerializedSet(t, node, point); len(served) != legacy {
						t.Fatalf("served %d entries, the old binary's finished legacy bucket has %d", len(served), legacy)
					}
					if health, e := node.CheckUTXOHealth(point); e != nil || !health.Ready || !health.Verified {
						t.Fatalf("health after re-upgrade %+v %v", health, e)
					}
				}
				pp42MineMove(t, raw, node)
				pp42RequireCountExact(t, node, raw, &prefix.Prefix{})
				headerOnly, withBody := pp42PastBodies(t, node)
				if headerOnly == 0 || withBody != 0 {
					t.Fatalf("below the pruning point: %d header-only blocks, %d with a body", headerOnly, withBody)
				}
				node.Close()
				pp42RequireNoWorkerGoroutine(t)
				if e = raw.Close(); e != nil {
					t.Fatal(e)
				}
			})
		}
	}
}
