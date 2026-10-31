//go:build pruningtest

package consensus_test

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus"
	consensusdatabase "github.com/HoosatNetwork/HTND/v2/domain/consensus/database"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/datastructures/blockstore"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/processes/pruningmanager"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
	"github.com/HoosatNetwork/HTND/v2/util/staging"
)

// TestC4OffLockEvictionRace (C4-OFFLOCK-EVICTION R1) is meant for -race. History deletion
// evicts each batch from the six stores' caches without the consensus lock, so:
//   - counts: block commits (the count by delta under the block store's lock) race
//     EvictDeleted on the real block store; Count() must come out exact;
//   - archival=false/true: during a whole job of an owner node, a goroutine reads every
//     planned block through all six stores under the consensus lock and lock-free
//     (consensus.HistoryViewForTest) plus the tips and a template, while another inserts
//     blocks. From publication on, every planned block must read as the legacy twin after
//     its single-commit deletion, also after the mask is removed (no stale cache entry).
//     After the job the databases equal the twin's, Count() equals the stored bodies, also
//     after a restart, and Close leaves no history deletion owner.
func TestC4OffLockEvictionRace(t *testing.T) {
	t.Run("counts", testC4OffLockCounts)
	defer func(k int) { pruningmanager.HistoryDeletionBatchBlocks = k }(pruningmanager.HistoryDeletionBatchBlocks)
	pruningmanager.HistoryDeletionBatchBlocks = 3
	for _, archival := range []bool{false, true} {
		t.Run(fmt.Sprintf("archival=%t", archival), func(t *testing.T) { testC4OffLockJob(t, archival) })
	}
}

func testC4OffLockCounts(t *testing.T) {
	db := pp42OpenPebble(t)
	defer db.Close()
	dbm := consensusdatabase.New(db)
	store, e := blockstore.New(dbm, consensusdatabase.MakeBucket([]byte{0}), 100, 1<<20, false)
	if e != nil {
		t.Fatal(e)
	}
	hash := func(i int) *externalapi.DomainHash {
		var b [externalapi.DomainHashSize]byte
		binary.LittleEndian.PutUint64(b[:], uint64(i)+1)
		return externalapi.NewDomainHashFromByteArray(&b)
	}
	block := dagconfig.TestnetParams.GenesisBlock
	commit := func(from, to int) error {
		for i := from; i < to; i++ {
			stagingArea := model.NewStagingArea()
			store.Stage(stagingArea, hash(i), block)
			if e := staging.CommitAllChanges(dbm, stagingArea); e != nil {
				return e
			}
		}
		return nil
	}
	const n = 2000
	if e = commit(0, n); e != nil {
		t.Fatal(e)
	}
	evictor := store.(interface {
		EvictDeleted(blockHashes []*externalapi.DomainHash)
	})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			evictor.EvictDeleted([]*externalapi.DomainHash{hash(i)})
		}
	}()
	e = commit(n, 2*n)
	wg.Wait()
	if e != nil {
		t.Fatal(e)
	}
	if got := store.Count(model.NewStagingArea()); got != n {
		t.Fatalf("Count() %d after %d bodies committed and %d evicted concurrently, want %d", got, 2*n, n, n)
	}
}

// c4OffLockView is the oracle of the job: its plan and the legacy twin's view of each block.
type c4OffLockView struct {
	plan []*externalapi.DomainHash
	post map[externalapi.DomainHash]consensus.HistoryView
}

func testC4OffLockJob(t *testing.T, archival bool) {
	defer constants.ForceSetBlockVersion(1)
	cfg := pp42CrashConfig()
	cfg.IsArchival = archival
	var errorsSeen atomic.Int32
	fail := func(format string, args ...interface{}) {
		if errorsSeen.Add(1) <= 10 {
			t.Errorf(format, args...)
		}
	}
	var node atomic.Value
	var twin *pp42Twin
	var n *pruningMoveNode
	var armed, claimed atomic.Bool
	published, goAhead, retired := make(chan c4OffLockView, 1), make(chan struct{}), make(chan struct{})
	var view c4OffLockView // owner hook goroutine only, until sent
	hook := func(p string) {
		c, ok := node.Load().(externalapi.Consensus)
		if !ok || !armed.Load() {
			return
		}
		switch p {
		case "history-planned":
			if !claimed.CompareAndSwap(false, true) {
				return
			}
			plan, _, found, e := consensus.HistoryDeletionPlanForTest(c)
			if e != nil || !found || len(plan) < 2*pruningmanager.HistoryDeletionBatchBlocks {
				fail("plan: %d blocks, found %t, %v", len(plan), found, e)
				return
			}
			point, e := c.PruningPoint()
			if e != nil {
				fail("pruning point: %v", e)
				return
			}
			for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(time.Millisecond) {
				twinPoint, e := twin.node.PruningPoint()
				if e == nil && twinPoint.Equal(point) && twin.drained(t, n) {
					break
				}
				if time.Now().After(deadline) {
					fail("the legacy twin never reached the move to %s", point)
					return
				}
			}
			view = c4OffLockView{plan: plan, post: map[externalapi.DomainHash]consensus.HistoryView{}}
			for _, h := range plan {
				if view.post[*h], e = consensus.HistoryViewForTest(twin.node, h); e != nil {
					fail("legacy view of %s: %v", h, e)
				}
			}
		case "history-mask-published":
			if view.plan != nil {
				published <- view
				<-goAhead
			}
		case "history-batch":
			if view.plan != nil {
				// Lets several reads and inserts run between two evictions.
				time.Sleep(20 * time.Millisecond)
			}
		case "history-mask-removed":
			if view.plan != nil {
				view.plan = nil
				close(retired)
			}
		}
	}
	n, db, closeNode := newPP42PhaseNode(t, cfg, hook)
	defer closeNode()
	node.Store(n.node)
	var legacyNode atomic.Value
	twin = newPP42Twin(t, n, pp42OpenPebble(t), true, archival, func(p string) {
		if p == "history-recorded" {
			if c, ok := legacyNode.Load().(externalapi.Consensus); ok {
				if e := consensus.RunLegacyHistoryDeletionForTest(c); e != nil {
					panic(e)
				}
			}
		}
	})
	legacyNode.Store(twin.node)
	var mu sync.Mutex // the node's and the twin's inserts go together
	insert := func(b *externalapi.DomainBlock) error {
		mu.Lock()
		defer mu.Unlock()
		if e := n.insertAndUpdate(b); e != nil {
			return e
		}
		if e := twin.node.ValidateAndInsertBlock(b, true, true); e != nil {
			return e
		}
		return twin.node.UpdatePruningPointIfRequired()
	}
	drained := func() bool {
		return twin.drained(t, n) && !n.updating() && !historyDeletionPending(t, n.db, n.dbPrefix)
	}
	n.insertUntilThePruningPointMoves(insert, nil)
	waitUntil(t, "both nodes drained the first move", drained)

	armed.Store(true)
	n.insertUntilThePruningPointMoves(insert, nil)
	var job c4OffLockView
	select {
	case job = <-published:
	case <-time.After(2 * time.Minute):
		t.Fatal("no history deletion job published its mask")
	}
	c := n.node
	var wg sync.WaitGroup
	var reads, inserts atomic.Int32
	wg.Add(2)
	go func() { // locked and lock-free readers until one pass after the mask is removed
		defer wg.Done()
		for last := false; !last; {
			select {
			case <-retired:
				last = true
			default:
			}
			for _, h := range job.plan {
				if v, e := consensus.HistoryViewForTest(c, h); e != nil || v != job.post[*h] {
					fail("planned block %s reads %+v (%v), legacy %+v (after retire: %t)", h, v, e, job.post[*h], last)
				}
			}
			tips, e := c.Tips()
			if e != nil {
				fail("tips: %v", e)
			}
			for _, h := range tips {
				if _, e := consensus.HistoryViewForTest(c, h); e != nil {
					fail("tip %s: %v", h, e)
				}
			}
			if _, e := c.BuildBlockTemplate(&externalapi.DomainCoinbaseData{
				ScriptPublicKey: &externalapi.ScriptPublicKey{Script: []byte{0x51}}}, nil); e != nil {
				fail("template: %v", e)
			}
			reads.Add(1)
		}
	}()
	go func() { // foreground block inserts until the mask is removed
		defer wg.Done()
		for {
			select {
			case <-retired:
				return
			default:
			}
			if e := insert(n.nextBlock()); e != nil {
				fail("foreground insert: %v", e)
				return
			}
			inserts.Add(1)
		}
	}()
	close(goAhead)
	select {
	case <-retired:
	case <-time.After(2 * time.Minute):
		t.Fatal("the job never removed its mask")
	}
	wg.Wait()
	if t.Failed() {
		t.FailNow()
	}
	t.Logf("%d planned blocks; %d reader passes, %d foreground inserts during the job", len(job.plan), reads.Load(), inserts.Load())
	if reads.Load() < 2 || inserts.Load() == 0 {
		t.Fatalf("the job ran without concurrent readers (%d passes) or inserts (%d)", reads.Load(), inserts.Load())
	}
	waitUntil(t, "both nodes drained", drained)
	pp42RequireSameDump(t, "after the job", pp42Dump(t, db, n.dbPrefix), pp42Dump(t, twin.db, n.dbPrefix))
	pp42RequireCountExact(t, n.node, db, n.dbPrefix)

	owners := func() int {
		buf := make([]byte, 8<<20)
		return bytes.Count(buf[:runtime.Stack(buf, true)], []byte(".startHistoryDeletionOwner.func1("))
	}
	before := owners()
	n.node.Close()
	if after := owners(); before == 0 || after != before-1 {
		t.Fatalf("history deletion owner goroutines: %d before Close, %d after", before, after)
	}
	var e error
	if n.node, _, e = consensus.NewFactory().NewConsensus(n.nodeConfig, db, n.dbPrefix, nil); e != nil {
		t.Fatal(e)
	}
	pp42RequireCountExact(t, n.node, db, n.dbPrefix)
}
