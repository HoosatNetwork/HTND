//go:build pruningtest

package consensus_test

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/processes/pruningmanager"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/ruleerrors"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database"
)

// pp42MaskWindows runs the locked and lock-free readers of D12′ in every window of one
// history deletion job on a c′ node, against a legacy twin that took the same blocks and
// deleted the same history in one staging commit.
type pp42MaskWindows struct {
	t        *testing.T
	archival bool
	n        *pruningMoveNode
	node     atomic.Value // externalapi.Consensus: the c′ node
	twin     *pp42Twin
	mu       sync.Mutex // serializes the windows' foreground inserts with the test's own

	side        *externalapi.DomainBlock // a side tip the job deletes as a pruned tip
	sideChild   *externalapi.DomainBlock // a block whose only parent is side
	trustedSide *externalapi.BlockWithTrustedData
	armed       atomic.Bool
	running     atomic.Bool // the job deleting side has begun and not finished its windows
	active      bool        // as running; only the owner's hook goroutine uses the rest
	done        atomic.Bool
	hung        atomic.Bool
	errors      atomic.Int32

	plan            []*externalapi.DomainHash
	pre, post       map[externalapi.DomainHash]consensus.HistoryView
	batch, sideAt   int
	probe           *externalapi.DomainHash // a planned block given a disqualified status
	childInserted   bool
	postSeen        int
	windows         []string
	reinsertRefused int
}

func (w *pp42MaskWindows) fail(format string, args ...interface{}) {
	if w.errors.Add(1) <= 10 {
		w.t.Errorf(format, args...)
	}
}

func (w *pp42MaskWindows) insert(b *externalapi.DomainBlock) error {
	if e := w.n.insertAndUpdate(b); e != nil {
		return e
	}
	if e := w.twin.node.ValidateAndInsertBlock(b, true, true); e != nil {
		return e
	}
	return w.twin.node.UpdatePruningPointIfRequired()
}

func (w *pp42MaskWindows) hook(p string) {
	c, ok := w.node.Load().(externalapi.Consensus)
	if !ok || !w.armed.Load() || w.done.Load() || w.hung.Load() {
		return
	}
	switch p {
	case "history-planned":
		if !w.active {
			w.begin(c)
		}
	case "history-mask-published", "history-batch-commit", "history-last-batch-commit", "history-batch", "history-mask-removed":
	case "history-before-batch-commit":
		if w.active {
			w.batch++
		}
	default:
		return
	}
	if !w.active {
		return
	}
	w.window(c, p)
	if p == "history-batch" && w.batch*pruningmanager.HistoryDeletionBatchBlocks >= len(w.plan) {
		w.active = false
		w.done.Store(true)
		w.running.Store(false)
	}
}

// begin arms the windows for the job whose plan holds the side tip, and takes the
// oracle: the c′ node's own view of every planned block before publication, and the
// legacy twin's view after its single-commit deletion.
func (w *pp42MaskWindows) begin(c externalapi.Consensus) {
	plan, _, found, e := consensus.HistoryDeletionPlanForTest(c)
	if e != nil || !found {
		w.fail("plan: %v %t", e, found)
		return
	}
	side := consensushashing.BlockHash(w.side)
	w.sideAt = -1
	for i, h := range plan {
		if h.Equal(side) {
			w.sideAt = i
		}
	}
	if w.sideAt < 0 {
		return
	}
	point, e := c.PruningPoint()
	if e != nil {
		w.fail("pruning point: %v", e)
		return
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		twinPoint, e := w.twin.node.PruningPoint()
		if e == nil && twinPoint.Equal(point) && w.twin.drained(w.t, w.n) {
			break
		}
		if time.Now().After(deadline) {
			w.fail("the legacy twin never reached the move to %s", point)
			return
		}
		time.Sleep(time.Millisecond)
	}
	w.plan, w.active, w.batch = plan, true, 0
	w.running.Store(true)
	w.pre, w.post = map[externalapi.DomainHash]consensus.HistoryView{}, map[externalapi.DomainHash]consensus.HistoryView{}
	for _, h := range plan {
		if w.pre[*h], e = consensus.HistoryViewForTest(c, h); e != nil {
			w.fail("view of %s: %v", h, e)
		}
		if w.post[*h], e = consensus.HistoryViewForTest(w.twin.node, h); e != nil {
			w.fail("legacy view of %s: %v", h, e)
		}
		if w.pre[*h] == w.post[*h] {
			w.fail("planned block %s reads the same before and after the deletion: %+v", h, w.pre[*h])
		}
	}
	// The last planned block, deleted by the last batch, gets a disqualified status now,
	// after its pre view: without the status mask the repair walk would reset it.
	w.probe = plan[len(plan)-1]
}

// window runs the readers of one window and compares them with the oracle.
func (w *pp42MaskWindows) window(c externalapi.Consensus, p string) {
	label := fmt.Sprintf("%s:%d", p, w.batch)
	w.windows = append(w.windows, label)
	published := p != "history-planned"
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.readers(c, label, published)
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		w.hung.Store(true)
		w.fail("%s: a reader hung under the consensus lock", label)
		return
	}
	if p == "history-planned" {
		if e := consensus.StageStatusForTest(c, w.probe, externalapi.StatusDisqualifiedFromChain); e != nil {
			w.fail("disqualifying %s: %v", w.probe, e)
		}
	}
	w.sideBody(c, label, p)
	if p == "history-mask-published" || p == "history-batch" {
		w.mu.Lock()
		e := w.insert(w.n.nextBlock())
		w.mu.Unlock()
		if e != nil {
			w.fail("%s: foreground insert: %v", label, e)
		}
	}
}

func (w *pp42MaskWindows) readers(c externalapi.Consensus, label string, published bool) {
	// Every planned block reads as before the job until the mask is published, and as the
	// legacy twin after its deletion from then on: never anything else, never back.
	postSeen := 0
	for _, h := range w.plan {
		v, e := consensus.HistoryViewForTest(c, h)
		if e != nil {
			w.fail("%s: view of %s: %v", label, h, e)
			continue
		}
		want := w.pre[*h]
		if published {
			want = w.post[*h]
		}
		if v != want {
			w.fail("%s: planned block %s reads %+v, want %+v (published %t)", label, h, v, want, published)
		}
		if v == w.post[*h] {
			postSeen++
		}
		_, e = c.GetBlockAcceptanceData(h)
		_, eTwin := w.twin.node.GetBlockAcceptanceData(h)
		if published && (e == nil) != (eTwin == nil) {
			w.fail("%s: acceptance data of %s: %v, legacy %v", label, h, e, eTwin)
		}
		if e != nil && !errors.Is(e, database.ErrNotFound) {
			w.fail("%s: acceptance data of %s: %v", label, h, e)
		}
	}
	if postSeen < w.postSeen {
		w.fail("%s: %d planned blocks read deleted, %d before", label, postSeen, w.postSeen)
	}
	w.postSeen = postSeen

	// The zero-tips fallback walks every stored block under the lock.
	highest, e := consensus.FindHighestValidBlockForTest(c)
	twinHighest, eTwin := consensus.FindHighestValidBlockForTest(w.twin.node)
	if e != nil || eTwin != nil || !highest.Equal(twinHighest) {
		w.fail("%s: FindHighestValidBlock %s %v, legacy %s %v", label, highest, e, twinHighest, eTwin)
	}

	// The anticone serve: the set under the lock, the bodies without it.
	anticone, e := c.PruningPointAndItsAnticone()
	twinAnticone, eTwin := w.twin.node.PruningPointAndItsAnticone()
	if e != nil || eTwin != nil || fmt.Sprint(pp42SortedHashes(anticone)) != fmt.Sprint(pp42SortedHashes(twinAnticone)) {
		w.fail("%s: pruning point anticone %v %v, legacy %v %v", label, anticone, e, twinAnticone, eTwin)
	}
	for _, h := range anticone {
		if block, found, e := c.GetBlock(h); e != nil || !found || block == nil {
			w.fail("%s: serving anticone block %s: found %t, %v", label, h, found, e)
		}
	}
	tip := w.n.tip
	if _, e := c.GetAnticone(w.n.pruningPoint(), tip, 1<<20); e != nil {
		w.fail("%s: anticone: %v", label, e)
	}
	if _, e := c.BuildBlockTemplate(&externalapi.DomainCoinbaseData{
		ScriptPublicKey: &externalapi.ScriptPublicKey{Script: []byte{0x51}}}, nil); e != nil {
		w.fail("%s: template: %v", label, e)
	}

	// The repair walks: from the tips, and from a planned block whose stored status is
	// disqualified until its batch commits.
	reset, e := c.RepairDisqualifiedTipChains()
	twinReset, eTwin := w.twin.node.RepairDisqualifiedTipChains()
	if e != nil || eTwin != nil || reset != twinReset {
		w.fail("%s: RepairDisqualifiedTipChains %d %v, legacy %d %v", label, reset, e, twinReset, eTwin)
	}
	if published {
		reset, e := consensus.RepairDisqualifiedChainsFromForTest(c, []*externalapi.DomainHash{w.probe})
		twinReset, eTwin := consensus.RepairDisqualifiedChainsFromForTest(w.twin.node, []*externalapi.DomainHash{w.probe})
		if e != nil || eTwin != nil || reset != twinReset {
			w.fail("%s: repair walk from planned %s reset %d %v, legacy %d %v", label, w.probe, reset, e, twinReset, eTwin)
		}
	}
}

// sideBody inserts the side tip's child (a masked parent) once, and the side tip's body
// again around its batch's commit, in both orders.
func (w *pp42MaskWindows) sideBody(c externalapi.Consensus, label, p string) {
	side := consensushashing.BlockHash(w.side)
	sideBatch := w.sideAt/pruningmanager.HistoryDeletionBatchBlocks + 1
	if p == "history-mask-published" && !w.childInserted {
		w.childInserted = true
		w.mu.Lock()
		e := c.ValidateAndInsertBlock(w.sideChild, true, true)
		eTwin := w.twin.node.ValidateAndInsertBlock(w.sideChild, true, true)
		w.mu.Unlock()
		if fmt.Sprint(e) != fmt.Sprint(eTwin) {
			w.fail("%s: insert with a masked parent: %v, legacy %v", label, e, eTwin)
		}
		w.t.Logf("%s: the masked parent's child: %v", label, e)
	}
	reinsert := (p == "history-before-batch-commit" || p == "history-batch-commit") && w.batch == sideBatch
	if reinsert {
		// Pruned: the legacy twin would take the body now; the c′ node refuses it while the
		// job deletes the block, whichever side of the batch's commit it arrives on.
		// Archival: the body is still stored, a duplicate on both nodes.
		w.mu.Lock()
		e := c.ValidateAndInsertBlock(w.side, true, true)
		var eTwin error
		if w.archival {
			eTwin = w.twin.node.ValidateAndInsertBlock(w.side, true, true)
		}
		w.mu.Unlock()
		if (!w.archival && !errors.Is(e, ruleerrors.ErrPrunedBlock)) ||
			(w.archival && (!errors.Is(e, ruleerrors.ErrDuplicateBlock) || fmt.Sprint(e) != fmt.Sprint(eTwin))) {
			w.fail("%s: body of the masked side tip: %v (legacy %v), want refused as pruned (archival: duplicate)",
				label, e, eTwin)
		}
		w.reinsertRefused++
		if v, e := consensus.HistoryViewForTest(c, side); e != nil || v != w.post[*side] {
			w.fail("%s: side tip after its refused body: %+v %v, want %+v", label, v, e, w.post[*side])
		}
	}
	if p == "history-batch" && w.batch == sideBatch {
		w.mu.Lock()
		e := c.ValidateAndInsertBlockWithTrustedData(w.trustedSide, false)
		w.mu.Unlock()
		if (!w.archival && !errors.Is(e, ruleerrors.ErrPrunedBlock)) ||
			(w.archival && !errors.Is(e, ruleerrors.ErrDuplicateBlock)) {
			w.fail("%s: trusted body after eviction: %v, want soft refusal (archival: duplicate)", label, e)
		}
	}
	if p == "history-mask-removed" {
		w.mu.Lock()
		e := c.ValidateAndInsertBlock(w.side, true, true)
		eTwin := w.twin.node.ValidateAndInsertBlock(w.side, true, true)
		w.mu.Unlock()
		if fmt.Sprint(e) != fmt.Sprint(eTwin) {
			w.fail("%s: body of the side tip after the job: %v, legacy %v", label, e, eTwin)
		}
		v, e1 := consensus.HistoryViewForTest(c, side)
		vTwin, e2 := consensus.HistoryViewForTest(w.twin.node, side)
		if e1 != nil || e2 != nil || v != vTwin {
			w.fail("%s: side tip after its body: %+v %v, legacy %+v %v", label, v, e1, vTwin, e2)
		}
		w.post[*side] = vTwin
		w.t.Logf("%s: the side tip's body after the job: %v", label, e)
	}
}

func pp42SortedHashes(hashes []*externalapi.DomainHash) []*externalapi.DomainHash {
	sorted := append([]*externalapi.DomainHash(nil), hashes...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Less(sorted[j]) })
	return sorted
}

// TestC4HistoryMaskWindows is D12′, gate B of panel-42 SYNTHESIS, for c′: the whole plan
// is published as a logical delete in one hold, the batches commit without the lock,
// each is evicted without the lock, and one hold after the last eviction removes the mask. At every window
// (history-planned, -mask-published, and per batch -before-batch-commit, -batch-commit,
// -last-batch-commit, -batch, then -mask-removed) the test runs, against a legacy twin that took the same
// blocks and deleted the same history in one staging commit:
//   - every store a deletion touches, for every planned block, through the locked cached
//     getters and the lock-free paths: the c′ node's own pre-job state before publication,
//     the legacy twin's post-deletion state from publication on, never anything else, and
//     the number of planned blocks read deleted never decreases;
//   - locked GetBlockAcceptanceData (not found where the twin has none);
//   - FindHighestValidBlock (the zero-tips fallback, run directly) under a timeout;
//   - the anticone serve (set under the lock, bodies lock-free), GetAnticone, a template;
//   - RepairDisqualifiedTipChains, and its walk from a planned block whose stored status
//     is disqualified until its batch commits;
//   - an insert whose only parent is a masked side tip, compared with the twin;
//   - the side tip's body inserted again before and after its batch's commit (pruned:
//     refused as pruned while the job runs; archival: a duplicate, as on the twin), and
//     after the job (as the twin);
//   - a foreground block at publication and after each batch.
//
// After the job the databases equal the twin's (pp42Dump) with the count rule.
func TestC4HistoryMaskWindows(t *testing.T) {
	defer func(k int) { pruningmanager.HistoryDeletionBatchBlocks = k }(pruningmanager.HistoryDeletionBatchBlocks)
	pruningmanager.HistoryDeletionBatchBlocks = 3
	for _, archival := range []bool{false, true} {
		t.Run(fmt.Sprintf("archival=%t", archival), func(t *testing.T) {
			defer constants.ForceSetBlockVersion(1)
			cfg := pp42CrashConfig()
			cfg.IsArchival = archival
			// One parent per block, the virtual included: a side tip then leaves the virtual's
			// past, and the next move prunes it as a tip, so its job deletes it from a pruned tip.
			cfg.MaxBlockParents = make([]externalapi.KType, len(cfg.MaxBlockParents))
			for i := range cfg.MaxBlockParents {
				cfg.MaxBlockParents[i] = 1
			}
			w := &pp42MaskWindows{t: t, archival: archival}
			n, db, closeNode := newPP42PhaseNode(t, cfg, w.hook)
			defer closeNode()
			w.n = n
			w.node.Store(n.node)
			var legacyNode atomic.Value
			w.twin = newPP42Twin(t, n, pp42OpenPebble(t), true, archival, func(p string) {
				if p == "history-recorded" {
					if node, ok := legacyNode.Load().(externalapi.Consensus); ok {
						if e := consensus.RunLegacyHistoryDeletionForTest(node); e != nil {
							panic(e)
						}
					}
				}
			})
			legacyNode.Store(w.twin.node)
			insert := func(b *externalapi.DomainBlock) error {
				w.mu.Lock()
				defer w.mu.Unlock()
				return w.insert(b)
			}
			drained := func() bool {
				return w.twin.drained(t, n) && !n.updating() && !historyDeletionPending(t, n.db, n.dbPrefix)
			}
			n.insertUntilThePruningPointMoves(insert, nil)
			waitUntil(t, "both nodes drained the first move", drained)

			// A side tip off the chain, and its child, built while the side tip is recent.
			sideHash, _, e := n.source.AddBlock([]*externalapi.DomainHash{n.tip}, nil, nil)
			if e != nil {
				t.Fatal(e)
			}
			side, found, e := n.source.GetBlock(sideHash)
			if e != nil || !found {
				t.Fatal(found, e)
			}
			child, _, e := n.source.BuildBlockWithParents([]*externalapi.DomainHash{sideHash}, nil, nil)
			if e != nil {
				t.Fatal(e)
			}
			w.side, w.sideChild = side, child
			data, e := n.source.TrustedGHOSTDAGData(sideHash)
			if e != nil {
				t.Fatal(e)
			}
			w.trustedSide = &externalapi.BlockWithTrustedData{Block: side,
				GHOSTDAGData: []*externalapi.BlockGHOSTDAGDataHashPair{{Hash: sideHash, GHOSTDAGData: data}}}
			if e = insert(side); e != nil {
				t.Fatal(e)
			}
			w.armed.Store(true)
			for move := 0; move < 6 && !w.done.Load() && !w.hung.Load(); move++ {
				n.insertUntilThePruningPointMoves(insert, nil)
				// The job's record is gone before its last windows run: wait for them too.
				waitUntil(t, "both nodes drained the move", func() bool {
					return w.done.Load() || (drained() && !w.running.Load())
				})
			}
			if w.hung.Load() {
				t.Fatal("a reader hung; the node cannot be closed")
			}
			if !w.done.Load() {
				t.Fatalf("no job deleted the side tip %s in its plan", sideHash)
			}
			waitUntil(t, "both nodes drained", drained)
			if w.reinsertRefused != 2 {
				t.Fatalf("the side tip's body was inserted again %d times while masked, want 2 (both commit orders)",
					w.reinsertRefused)
			}
			pp42RequireSameDump(t, "after the job", pp42Dump(t, db, n.dbPrefix), pp42Dump(t, w.twin.db, n.dbPrefix))
			pp42RequireCountMatchesBodies(t, db, n.dbPrefix)
			legacyCount, _, _, _ := pp42Counts(t, w.twin.db, n.dbPrefix)
			if got := pp42StoredCount(t, db, n.dbPrefix); got != legacyCount {
				t.Fatalf("stored count %d, legacy blocks-count %d", got, legacyCount)
			}
			windows := make(map[string]int)
			for _, label := range w.windows {
				phase, _, _ := strings.Cut(label, ":")
				windows[phase]++
			}
			batches := (len(w.plan) + pruningmanager.HistoryDeletionBatchBlocks - 1) / pruningmanager.HistoryDeletionBatchBlocks
			for phase, want := range map[string]int{
				"history-planned": 1, "history-mask-published": 1,
				"history-before-batch-commit": batches, "history-batch-commit": batches,
				"history-last-batch-commit": 1, "history-batch": batches, "history-mask-removed": 1,
			} {
				if got := windows[phase]; got != want {
					t.Errorf("reader window %s observed %d times, want %d", phase, got, want)
				}
			}
			t.Logf("%d planned blocks (side tip at %d), windows %v", len(w.plan), w.sideAt, w.windows)
		})
	}
}
