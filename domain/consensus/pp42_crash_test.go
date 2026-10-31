//go:build pruningtest

package consensus_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/database/serialization"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/datastructures/pruningstore"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/processes/pruningmanager"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/utxo"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
	"github.com/HoosatNetwork/HTND/v2/domain/prefixmanager/prefix"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database/pebble"
)

type pp42Expected struct {
	Target  string
	Entries [][]byte
}

func pp42CrashConfig() *consensus.Config {
	c := &consensus.Config{Params: dagconfig.TestnetParams, DeferPruningPointUTXOSetUpdate: true}
	// The crash child inherits this, so the killed node and its reopen agree.
	c.IsArchival = os.Getenv("PP42_ARCHIVAL") == "1"
	c.SkipProofOfWork = true
	c.DisableDifficultyAdjustment = true
	c.POWScores = []uint64{math.MaxUint64}
	c.K = append([]externalapi.KType(nil), c.K...)
	c.K[0] = 0
	c.FinalityDuration = []time.Duration{30 * c.TargetTimePerBlock[0]}
	c.PruningProofM = 1
	c.DifficultyAdjustmentWindowSize = make([]int, len(c.DifficultyAdjustmentWindowSize))
	for i := range c.DifficultyAdjustmentWindowSize {
		c.DifficultyAdjustmentWindowSize[i] = 10
	}
	return c
}
func pp42SerializedSet(t *testing.T, node externalapi.Consensus, target *externalapi.DomainHash) [][]byte {
	t.Helper()
	pairs, e := node.GetPruningPointUTXOs(target, nil, 10000)
	if e != nil {
		t.Fatal(e)
	}
	var entries [][]byte
	for _, p := range pairs {
		v, e := utxo.SerializeUTXO(p.UTXOEntry, p.Outpoint)
		if e != nil {
			t.Fatal(e)
		}
		entries = append(entries, v)
	}
	sort.Slice(entries, func(i, j int) bool { return bytes.Compare(entries[i], entries[j]) < 0 })
	return entries
}

// pp42NextMove is what the old path produced for the move after the crash target,
// together with the blocks that make it. Stored apart from expected.json, which the
// pinned legacy probe also reads.
type pp42NextMove struct {
	Commitment, Next, NextCommitment string
	NextEntries                      [][]byte
	Blocks                           [][]byte
}

// Unlike the lower-level journal matrix, this kills a complete consensus instance,
// reopens through the real factory/startup path, and compares with the old writer on
// exactly the same blocks. Parent never asks the killed process to flush or close.
// After recovery the next move must also succeed and match the old writer.
func TestPP42ConsensusCrash(t *testing.T) {
	if os.Getenv("PP42_NODE_CRASH_DIR") != "" {
		pp42CrashChild(t)
		return
	}
	defer constants.ForceSetBlockVersion(1)
	for _, point := range append(append([]string(nil), pp42CrashPoints...), pp42FallbackCrashPoints...) {
		t.Run(point, func(t *testing.T) {
			dir, want := pp42CrashAt(t, point)
			raw, node := pp42OpenRecovered(t, dir, want)
			pp42ContinueNextMove(t, raw, node, dir)
			node.Close()
			pp42RequireNoWorkerGoroutine(t)
			// Pebble refuses to close with a leaked snapshot.
			if e := raw.Close(); e != nil {
				t.Fatal(e)
			}
		})
	}
}

// Archival nodes (Config.IsArchival, htnd --archival) run the same journal worker.
// Kill at several phases, reopen: the recovered set and its commitment, and the next
// move, equal the old synchronous path; history past the pruning point only flips to
// header-only status and keeps its bodies. The non-archival control run shows the
// body check discriminates. The disk estimate is covered by TestPP42JournalDiskPolicy.
func TestPP42ArchivalJournal(t *testing.T) {
	defer constants.ForceSetBlockVersion(1)
	for _, archival := range []bool{true, false} {
		points := []string{"ready", "data-commit:2", "scan", "before-finish"}
		if !archival {
			points = points[:1]
		}
		for _, point := range points {
			t.Run(fmt.Sprintf("archival=%t/%s", archival, point), func(t *testing.T) {
				t.Setenv("PP42_ARCHIVAL", map[bool]string{true: "1", false: "0"}[archival])
				dir, want := pp42CrashAt(t, point)
				raw, node := pp42OpenRecovered(t, dir, want)
				pp42ContinueNextMove(t, raw, node, dir)
				headerOnly, withBody := pp42PastBodies(t, node)
				t.Logf("selected chain below the pruning point: %d header-only blocks, %d with bodies", headerOnly, withBody)
				if headerOnly == 0 {
					t.Fatal("no history below the pruning point was finished")
				}
				if archival && withBody != headerOnly {
					t.Fatalf("archival node lost %d block bodies", headerOnly-withBody)
				}
				if !archival && withBody != 0 {
					t.Fatalf("control: pruning node kept %d bodies", withBody)
				}
				node.Close()
				pp42RequireNoWorkerGoroutine(t)
				if e := raw.Close(); e != nil {
					t.Fatal(e)
				}
			})
		}
	}
}

// pp42PastBodies walks the selected chain from genesis (excluded) to the pruning point
// (excluded) and counts header-only blocks and how many of them still have a body.
func pp42PastBodies(t *testing.T, node externalapi.Consensus) (headerOnly, withBody int) {
	t.Helper()
	point, e := node.PruningPoint()
	if e != nil {
		t.Fatal(e)
	}
	chain, e := node.GetVirtualSelectedParentChainFromBlock(pp42CrashConfig().GenesisHash)
	if e != nil {
		t.Fatal(e)
	}
	for _, hash := range chain.Added {
		if hash.Equal(point) {
			break
		}
		info, e := node.GetBlockInfo(hash)
		if e != nil {
			t.Fatal(e)
		}
		if info.BlockStatus != externalapi.StatusHeaderOnly {
			continue
		}
		headerOnly++
		block, found, e := node.GetBlock(hash)
		if e != nil {
			t.Fatal(e)
		}
		if found && len(block.Transactions) > 0 {
			withBody++
		}
	}
	return headerOnly, withBody
}

// Every journal phase boundary reachable in a consensus instance. A batch of two
// entries makes the small test diffs span several build and apply batches, so
// ":2" points are kills between two committed batches of the same phase.
var pp42CrashPoints = []string{"preparing", "derive-acceptance", "cleanup", "journal-chunk", "journal-chunk:2", "ready",
	"before-data-commit", "data-commit", "before-data-commit:2", "data-commit:2", "complete", "scan", "before-finish", "finish"}

// The D1 disk fallback (journalDiskFallback): the journal cannot begin the move right
// after the COMPLETE base for disk space, so the move is written on the synchronous
// legacy path under the lock and the manifest adopts it (COMPLETE at the new index).
// "fallback-write" kills inside that write, after its first UTXO set write;
// "fallback" kills after the synced adoption. The reopened node has disk space again.
// Pruned nodes only: an archival node never takes the fallback.
var pp42FallbackCrashPoints = []string{"fallback-write", "fallback"}

func pp42CrashChild(t *testing.T) {
	defer constants.ForceSetBlockVersion(1)
	dir := os.Getenv("PP42_NODE_CRASH_DIR")
	pruningstore.JournalBatchEntries = 2
	pruningmanager.HistoryDeletionBatchBlocks = 2
	cfg := pp42CrashConfig()
	cfg.DeferPruningPointUTXOSetUpdate = false // independent old-path source
	point, nth := os.Getenv("PP42_NODE_CRASH_POINT"), int32(1)
	if parts := strings.Split(point, ":"); len(parts) == 2 {
		point = parts[0]
		v, e := strconv.Atoi(parts[1])
		if e != nil {
			t.Fatal(e)
		}
		nth = int32(v)
	}
	var armed atomic.Bool
	var seen atomic.Int32
	var db atomic.Pointer[pp42ViewDB]
	kill := func(p string) {
		if p == point && armed.Load() && seen.Add(1) == nth {
			// History deletion commits are NoSync, like the finish: a kill can lose them
			// with the WAL tail. Sync the WAL up to here, so the kill hits the state the
			// point names instead of an earlier one.
			if strings.HasPrefix(p, "history-") || p == "fallback-write" {
				pp42SyncWAL(db.Load())
			}
			if e := os.WriteFile(filepath.Join(dir, "barrier"), []byte(p), 0600); e != nil {
				panic(e)
			}
			select {}
		}
	}
	n, viewDB, _ := newPP42PhaseNode(t, cfg, kill)
	db.Store(viewDB)
	first := n.insertUntilThePruningPointMoves(n.insertAndUpdate, nil)
	n.waitUntilUpdated()
	for i := 0; i < 300; i++ {
		b := n.nextBlock()
		target, e := n.source.PruningPoint()
		if e != nil {
			t.Fatal(e)
		}
		if !target.Equal(first) {
			expected := pp42Expected{target.String(), pp42SerializedSet(t, n.source, target)}
			next := pp42NextMove{Commitment: pp42Commitment(t, n.source, target)}
			// The history the old path left right after the target's move, before the next move's blocks.
			pp42WriteJSON(t, filepath.Join(dir, "history.json"), pp42ChainHistory(t, n.source))
			for point := target; point.Equal(target); {
				if len(next.Blocks) == 400 {
					t.Fatal("next move missing")
				}
				block, e := serialization.DomainBlockToDbBlock(n.nextBlock()).MarshalVT()
				if e != nil {
					t.Fatal(e)
				}
				next.Blocks = append(next.Blocks, block)
				if point, e = n.source.PruningPoint(); e != nil {
					t.Fatal(e)
				}
				next.Next = point.String()
			}
			nextPoint, _ := externalapi.NewDomainHashFromString(next.Next)
			next.NextEntries, next.NextCommitment = pp42SerializedSet(t, n.source, nextPoint), pp42Commitment(t, n.source, nextPoint)
			pp42WriteJSON(t, filepath.Join(dir, "expected.json"), expected)
			pp42WriteJSON(t, filepath.Join(dir, "next.json"), next)
			if strings.HasPrefix(point, "fallback") {
				viewDB.avail.Store(1) // the journal's Begin of this move is refused
				if point == "fallback-write" {
					write := func() { kill(point) }
					viewDB.utxoWrite.Store(&write)
				}
			}
			armed.Store(true)
			if e = n.node.ValidateAndInsertBlock(b, true, true); e != nil {
				t.Fatal(e)
			}
			if e = n.node.UpdatePruningPointIfRequired(); e != nil {
				t.Fatal(e)
			}
			select {}
		}
		if e = n.insertAndUpdate(b); e != nil {
			t.Fatal(e)
		}
	}
	t.Fatal("second move missing")
}

func pp42Commitment(t *testing.T, node externalapi.Consensus, block *externalapi.DomainHash) string {
	t.Helper()
	header, e := node.GetBlockHeader(block)
	if e != nil {
		t.Fatal(e)
	}
	return header.UTXOCommitment().String()
}

func pp42WriteJSON(t *testing.T, path string, value interface{}) {
	t.Helper()
	data, e := json.Marshal(value)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(path, data, 0600); e != nil {
		t.Fatal(e)
	}
}

func pp42ReadJSON(t *testing.T, path string, value interface{}) {
	t.Helper()
	data, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(data, value); e != nil {
		t.Fatal(e)
	}
}

// pp42CrashAt runs the child until the named boundary and kills it there.
func pp42CrashAt(t *testing.T, point string) (string, pp42Expected) {
	t.Helper()
	t.Setenv("HTND_TEST_MODE", "0")
	t.Setenv("HTND_PEBBLE_DISABLE_WAL", "0")

	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestPP42ConsensusCrash$", "-test.timeout=120s")
	cmd.Env = append(os.Environ(), "PP42_NODE_CRASH_DIR="+dir, "PP42_NODE_CRASH_POINT="+point)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if e := cmd.Start(); e != nil {
		t.Fatal(e)
	}
	deadline := time.Now().Add(60 * time.Second)
	for {
		if _, e := os.Stat(filepath.Join(dir, "barrier")); e == nil {
			break
		}
		if time.Now().After(deadline) {
			cmd.Process.Kill()
			cmd.Wait()
			t.Fatalf("missing barrier: %s", output.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if e := cmd.Process.Kill(); e != nil {
		t.Fatal(e)
	}
	_ = cmd.Wait()
	var want pp42Expected
	pp42ReadJSON(t, filepath.Join(dir, "expected.json"), &want)
	return dir, want
}

// pp42OpenRecovered reopens the killed database with the real factory and requires
// the old path's exact serialized set and header commitment for the crash target.
func pp42OpenRecovered(t *testing.T, dir string, want pp42Expected) (*pebble.DB, externalapi.Consensus) {
	t.Helper()
	return pp42OpenRecoveredWithHook(t, dir, want, nil, nil)
}

// pp42OpenRecoveredWithHook is pp42OpenRecovered with hook as the reopened node's lab
// journal and history hook; opened gets the node as soon as the factory returns it,
// before recovery has drained.
func pp42OpenRecoveredWithHook(t *testing.T, dir string, want pp42Expected, hook func(string),
	opened func(externalapi.Consensus)) (*pebble.DB, externalapi.Consensus) {
	t.Helper()
	raw, e := pebble.OpenPebbleDB(dir, 8)
	if e != nil {
		t.Fatal(e)
	}
	before, _ := raw.Get(pp42StateKey())
	t.Logf("manifest before recovery: %s", before)
	t.Logf("history deletion job recorded before recovery: %t", historyDeletionPending(t, raw, &prefix.Prefix{}))
	cfg := pp42CrashConfig()
	cfg.PruningPointJournalTestHook = hook
	node, _, e := consensus.NewFactory().NewConsensus(cfg, raw, &prefix.Prefix{}, nil)
	if e != nil {
		raw.Close()
		t.Fatal(e)
	}
	if opened != nil {
		opened(node)
	}
	// Startup only starts the owner (L3); recovery finishes in the background.
	pp42WaitDrained(t, raw)
	after, _ := raw.Get(pp42StateKey())
	t.Logf("manifest after recovery: %s", after)
	target, e := externalapi.NewDomainHashFromString(want.Target)
	if e != nil {
		t.Fatal(e)
	}
	var next pp42NextMove
	pp42ReadJSON(t, filepath.Join(dir, "next.json"), &next)
	pp42RequireSet(t, node, target, want.Entries, next.Commitment)
	return raw, node
}

func pp42StateKey() *database.Key {
	return database.MakeBucket((&prefix.Prefix{}).Serialize()).Key([]byte("pruning-point-utxo-set-journal-state"))
}

// pp42RequireSet requires the exact sorted serialized set and a verified health
// answer whose computed multiset and header commitment both equal the old path's.
func pp42RequireSet(t *testing.T, node externalapi.Consensus, point *externalapi.DomainHash, entries [][]byte, commitment string) {
	t.Helper()
	got := pp42SerializedSet(t, node, point)
	if len(got) != len(entries) {
		t.Fatalf("served %d entries, old path %d", len(got), len(entries))
	}
	for i := range got {
		if !bytes.Equal(got[i], entries[i]) {
			t.Fatalf("served entry %d differs from the old path", i)
		}
	}
	health, e := node.CheckUTXOHealth(point)
	if e != nil || !health.Ready || !health.Verified || health.HeaderCommitment.String() != commitment ||
		health.SetMultiset.String() != commitment {
		t.Fatalf("health %+v %v, old-path commitment %s", health, e, commitment)
	}
}

// pp42ContinueNextMove inserts the old path's following blocks into the recovered
// node: the next move must finish and serve exactly the old path's next set.
func pp42ContinueNextMove(t *testing.T, raw *pebble.DB, node externalapi.Consensus, dir string) {
	t.Helper()
	var next pp42NextMove
	pp42ReadJSON(t, filepath.Join(dir, "next.json"), &next)
	for _, data := range next.Blocks {
		dbBlock := &serialization.DbBlock{}
		if e := dbBlock.UnmarshalVT(data); e != nil {
			t.Fatal(e)
		}
		block, e := serialization.DbBlockToDomainBlock(dbBlock)
		if e != nil {
			t.Fatal(e)
		}
		if e = node.ValidateAndInsertBlock(block, true, true); e != nil {
			t.Fatal(e)
		}
		if e = node.UpdatePruningPointIfRequired(); e != nil {
			t.Fatal(e)
		}
	}
	pp42WaitDrained(t, raw)
	point, e := node.PruningPoint()
	if e != nil || point.String() != next.Next {
		t.Fatalf("next move: point %s, old path %s, %v", point, next.Next, e)
	}
	pp42RequireSet(t, node, point, next.NextEntries, next.NextCommitment)
}

func pp42WaitDrained(t *testing.T, raw *pebble.DB) {
	t.Helper()
	key := database.MakeBucket((&prefix.Prefix{}).Serialize()).Key([]byte(updatingPruningPointUTXOSetKeyName))
	waitUntil(t, "the pruning point UTXO set update drained", func() bool {
		found, e := raw.Has(key)
		if e != nil {
			t.Fatal(e)
		}
		return !found && !historyDeletionPending(t, raw, &prefix.Prefix{})
	})
}

// pp42RequireNoWorkerGoroutine fails if the journal owner or its scanner goroutine
// survived consensus Close. Pruning tests do not run in parallel.
func pp42RequireNoWorkerGoroutine(t *testing.T) {
	t.Helper()
	buf := make([]byte, 8<<20)
	stacks := buf[:runtime.Stack(buf, true)]
	for _, frame := range []string{"RunJournalWorker", "startPruningPointUTXOSetScanner", "scanPruningPointUTXOSet"} {
		if bytes.Contains(stacks, []byte(frame)) {
			t.Fatalf("goroutine in %s outlived consensus Close:\n%s", frame, stacks)
		}
	}
}

// pp42SyncWAL makes every write committed so far durable: a synced commit syncs the
// WAL up to itself.
func pp42SyncWAL(db *pp42ViewDB) {
	tx, e := db.BeginUnindexed()
	if e == nil {
		if e = tx.Put(database.MakeBucket(nil).Key([]byte("pp42-crash-sync")), []byte{1}); e == nil {
			e = tx.(interface{ CommitSync() error }).CommitSync()
		}
	}
	if e != nil {
		panic(e)
	}
}
