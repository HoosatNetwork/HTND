package consensus_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/database/serialization"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/processes/pruningmanager"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/ruleerrors"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/testutils"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database"
)

// The tests below run a node the way the app does (DeferPruningPointUTXOSetUpdate, UpdatePruningPointIfRequired after
// every insertion) and look at the check of the pruning point UTXO set against its commitment, which runs in the
// background after the set is written. Each starts from a node whose pruning point moved once and finished its update,
// so the next move's past block deletion removes the blocks of a real previous pruning point.

const scanTestTimeout = 10 * time.Second

// newScanTestNode returns a node past its first pruning point move, with that move's update finished.
func newScanTestNode(t *testing.T, consensusConfig *consensus.Config, name string) (
	n *pruningMoveNode, firstPruningPoint *externalapi.DomainHash, teardown func(),
) {
	n, teardown = newPruningMoveNode(t, consensusConfig, name, true)
	firstPruningPoint = n.insertUntilThePruningPointMoves(n.insertAndUpdate, nil)
	n.waitUntilUpdated()
	return n, firstPruningPoint, teardown
}

// insertAndUpdate inserts a block the way the node's flows do: the insertion, then UpdatePruningPointIfRequired. It
// fails the test if that waits for the background check, which it must not.
func (n *pruningMoveNode) insertAndUpdate(block *externalapi.DomainBlock) error {
	return n.within("inserting a block", func() error {
		err := n.node.ValidateAndInsertBlock(block, true, true)
		if err != nil {
			return err
		}
		return n.node.UpdatePruningPointIfRequired()
	})
}

func (n *pruningMoveNode) within(what string, f func() error) error {
	done := make(chan error, 1)
	go func() { done <- f() }()
	select {
	case err := <-done:
		return err
	case <-time.After(scanTestTimeout):
		n.t.Fatalf("%s did not return within %s: it waits for the pruning point UTXO set check", what, scanTestTimeout)
		return nil
	}
}

func (n *pruningMoveNode) waitForHold(hold *scanHold) {
	n.t.Helper()
	select {
	case <-hold.reached:
	case <-time.After(scanTestTimeout):
		n.t.Fatalf("the background check of the pruning point UTXO set never started")
	}
}

func (n *pruningMoveNode) requireRefused(pruningPoints ...*externalapi.DomainHash) {
	n.t.Helper()
	for _, pruningPoint := range pruningPoints {
		_, err := n.node.GetPruningPointUTXOs(pruningPoint, nil, 1)
		if !errors.Is(err, ruleerrors.ErrWrongPruningPointHash) {
			n.t.Fatalf("GetPruningPointUTXOs(%s): got %v, want ErrWrongPruningPointHash", pruningPoint, err)
		}
	}
}

func (n *pruningMoveNode) requireServedAndVerified(pruningPoint *externalapi.DomainHash) {
	n.t.Helper()
	pairs, err := n.node.GetPruningPointUTXOs(pruningPoint, nil, 1000)
	if err != nil || len(pairs) == 0 {
		n.t.Fatalf("GetPruningPointUTXOs(%s): %d entries, %v", pruningPoint, len(pairs), err)
	}
	health, err := n.node.CheckUTXOHealth(pruningPoint)
	if err != nil {
		n.t.Fatalf("CheckUTXOHealth: %+v", err)
	}
	if !health.Verified {
		n.t.Fatalf("the served pruning point UTXO set does not match the commitment of %s: %+v", pruningPoint, health)
	}
}

// blockDeleted reports whether the block's body is gone, as the past block deletion of a pruning point move leaves it.
func (n *pruningMoveNode) blockDeleted(blockHash *externalapi.DomainHash) bool {
	info, err := n.node.GetBlockInfo(blockHash)
	if err != nil {
		n.t.Fatalf("GetBlockInfo: %+v", err)
	}
	return info.BlockStatus == externalapi.StatusHeaderOnly
}

func (n *pruningMoveNode) requireNoMismatch() {
	n.t.Helper()
	select {
	case err := <-n.mismatches:
		n.t.Fatalf("an honest node reported a pruning point UTXO set mismatch: %v", err)
	default:
	}
}

// TestPruningPointUTXOSetCheckLetsBlocksThrough pins that the check of the pruning point UTXO set against its
// commitment, minutes on mainnet, runs without the consensus lock: blocks are inserted while it runs, without writing
// the set again for each of them. Until it passes, the set is refused to peers and no past blocks are deleted; then
// the update finishes.
func TestPruningPointUTXOSetCheckLetsBlocksThrough(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		defer constants.ForceSetBlockVersion(1)
		n, firstPruningPoint, teardown := newScanTestNode(t, consensusConfig, "TestPruningPointUTXOSetCheckLetsBlocksThrough")
		defer teardown()

		hold := n.db.armHold()
		defer hold.Release()
		started := n.db.started
		pruningPoint := n.insertUntilThePruningPointMoves(n.insertAndUpdate, nil)
		n.waitForHold(hold)
		if n.db.started != started+1 {
			t.Fatalf("%d pruning point UTXO set updates ran for one move, want 1", n.db.started-started)
		}

		for i := 0; i < 20; i++ {
			err := n.insertAndUpdate(n.nextBlock())
			if err != nil {
				t.Fatalf("inserting block #%d during the check: %+v", i, err)
			}
		}
		if n.db.started != started+1 {
			t.Fatalf("blocks inserted during the check wrote the pruning point UTXO set again (%d updates)",
				n.db.started-started)
		}
		if !n.updating() {
			t.Fatalf("the update finished before its check did")
		}
		n.requireRefused(pruningPoint, firstPruningPoint)
		if n.blockDeleted(firstPruningPoint) {
			t.Fatalf("past blocks were deleted before the check passed")
		}

		hold.Release()
		n.waitUntilUpdated()
		n.requireServedAndVerified(pruningPoint)
		if !n.blockDeleted(firstPruningPoint) {
			t.Fatalf("the finished update did not delete the blocks of the previous pruning point %s", firstPruningPoint)
		}
		n.requireNoMismatch()
	})
}

// corruptPruningPointUTXOSet adds an entry that no block created to the stored pruning point UTXO set: a copy of an
// existing entry under an output index its transaction does not have. No diff ever removes it.
func (n *pruningMoveNode) corruptPruningPointUTXOSet() *database.Key {
	n.t.Helper()
	bucket := database.MakeBucket(n.dbPrefix.Serialize()).Bucket([]byte("pruning-point-utxo-set"))
	cursor, err := n.db.Database.Cursor(bucket)
	if err != nil {
		n.t.Fatalf("Cursor: %+v", err)
	}
	defer cursor.Close()
	if !cursor.First() {
		n.t.Fatalf("setup: the pruning point UTXO set is empty")
	}
	key, err := cursor.Key()
	if err != nil {
		n.t.Fatalf("Key: %+v", err)
	}
	value, err := cursor.Value()
	if err != nil {
		n.t.Fatalf("Value: %+v", err)
	}
	outpoint := &serialization.DbOutpoint{}
	err = outpoint.UnmarshalVT(key.Suffix())
	if err != nil {
		n.t.Fatalf("UnmarshalVT: %+v", err)
	}
	outpoint.Index = 0xfffff
	suffix, err := outpoint.MarshalVT()
	if err != nil {
		n.t.Fatalf("MarshalVT: %+v", err)
	}
	bogusKey := bucket.Key(suffix)
	err = n.db.Database.Put(bogusKey, value)
	if err != nil {
		n.t.Fatalf("Put: %+v", err)
	}
	return bogusKey
}

func (n *pruningMoveNode) waitForMismatch() error {
	n.t.Helper()
	select {
	case err := <-n.mismatches:
		return err
	case <-time.After(scanTestTimeout):
		n.t.Fatalf("the node did not stop on a pruning point UTXO set that does not match its commitment")
		return nil
	}
}

// TestPruningPointUTXOSetMismatchStopsTheNode pins what a pruning point UTXO set that does not match its commitment
// does: Config.OnPruningPointUTXOSetMismatch, which stops the node, is called once with a diagnostic naming the
// pruning point, both hashes and how the set was derived. The set is not served, no past blocks are deleted and the
// update stays pending. A restart checks the set again and stops again; once the set is right, it finishes.
func TestPruningPointUTXOSetMismatchStopsTheNode(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		defer constants.ForceSetBlockVersion(1)
		n, firstPruningPoint, teardown := newScanTestNode(t, consensusConfig, "TestPruningPointUTXOSetMismatchStopsTheNode")
		defer teardown()

		bogusKey := n.corruptPruningPointUTXOSet()
		pruningPoint := n.insertUntilThePruningPointMoves(n.insertAndUpdate, nil)
		err := n.waitForMismatch()
		if !errors.Is(err, pruningmanager.ErrPruningPointUTXOSetMismatch) {
			t.Fatalf("the mismatch was reported as %v, want ErrPruningPointUTXOSetMismatch", err)
		}
		header, headerErr := n.node.GetBlockHeader(pruningPoint)
		if headerErr != nil {
			t.Fatalf("GetBlockHeader: %+v", headerErr)
		}
		for _, want := range []string{
			"pruning point      " + pruningPoint.String(),
			"header commitment  " + header.UTXOCommitment().String(),
			"computed hash      ",
			"diff from previous pruning point " + firstPruningPoint.String(),
			"entries totalling",
			"VERDICT",
			"not served",
		} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the diagnostic does not contain %q:\n%s", want, err)
			}
		}

		// Blocks still arrive while the node stops; they must not write or check the set again.
		started := n.db.started
		for i := 0; i < 5; i++ {
			if err := n.insertAndUpdate(n.nextBlock()); err != nil {
				t.Fatalf("inserting a block after the mismatch: %+v", err)
			}
		}
		select {
		case err := <-n.mismatches:
			t.Fatalf("the mismatch was reported twice: %v", err)
		case <-time.After(100 * time.Millisecond):
		}
		if n.db.started != started {
			t.Fatalf("the pruning point UTXO set was written again after the mismatch")
		}
		if !n.updating() {
			t.Fatalf("the update is no longer pending after the mismatch")
		}
		methodKey := database.MakeBucket(n.dbPrefix.Serialize()).Key([]byte("pruning-point-utxo-set-update-method"))
		if hasMethod, err := n.db.Has(methodKey); err != nil || !hasMethod {
			t.Fatalf("the recorded diff method is gone after the mismatch: %t, %v", hasMethod, err)
		}
		n.requireRefused(pruningPoint, firstPruningPoint)
		if n.blockDeleted(firstPruningPoint) {
			t.Fatalf("past blocks were deleted although the set did not match")
		}

		// The restart writes and checks the set again, and stops again.
		n.restart()
		err = n.waitForMismatch()
		if !errors.Is(err, pruningmanager.ErrPruningPointUTXOSetMismatch) {
			t.Fatalf("after the restart the mismatch was reported as %v", err)
		}
		n.requireRefused(pruningPoint)
		if !n.updating() || n.blockDeleted(firstPruningPoint) {
			t.Fatalf("the restarted node finished the update of a set that does not match")
		}

		// With the set right again, the next restart finishes the update.
		err = n.db.Database.Delete(bogusKey)
		if err != nil {
			t.Fatalf("Delete: %+v", err)
		}
		n.restart()
		n.waitUntilUpdated()
		n.requireServedAndVerified(pruningPoint)
		if !n.blockDeleted(firstPruningPoint) {
			t.Fatalf("the finished update did not delete the blocks of the previous pruning point")
		}
		n.requireNoMismatch()
	})
}

// TestPruningPointUTXOSetMismatchWithoutDeferral pins the path tools and tests use, without
// DeferPruningPointUTXOSetUpdate: the insertion that moves the pruning point returns the mismatch, and nothing is
// deleted.
func TestPruningPointUTXOSetMismatchWithoutDeferral(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		defer constants.ForceSetBlockVersion(1)
		n, teardown := newPruningMoveNode(t, consensusConfig, "TestPruningPointUTXOSetMismatchWithoutDeferral", false)
		defer teardown()
		insert := func(block *externalapi.DomainBlock) error { return n.node.ValidateAndInsertBlock(block, true, true) }
		firstPruningPoint := n.insertUntilThePruningPointMoves(insert, nil)
		n.corruptPruningPointUTXOSet()

		var err error
		for i := 0; i < 300 && err == nil; i++ {
			err = insert(n.nextBlock())
		}
		if err == nil || !strings.Contains(err.Error(), "doesn't match it's UTXO commitment") {
			t.Fatalf("the insertion that moved the pruning point returned %v, want the commitment mismatch", err)
		}
		if !n.updating() || n.blockDeleted(firstPruningPoint) {
			t.Fatalf("the update of a set that does not match finished")
		}
		n.requireNoMismatch()
	})
}

// TestStopDuringThePruningPointUTXOSetCheck stops a node while the check of the pruning point UTXO set runs. Close
// returns without finishing the update, which stays pending, so the database can be closed and nothing was deleted.
// The restart writes the set again and checks it, and then finishes.
func TestStopDuringThePruningPointUTXOSetCheck(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		defer constants.ForceSetBlockVersion(1)
		n, firstPruningPoint, teardown := newScanTestNode(t, consensusConfig, "TestStopDuringThePruningPointUTXOSetCheck")
		defer teardown()

		hold := n.db.armHold()
		defer hold.Release()
		pruningPoint := n.insertUntilThePruningPointMoves(n.insertAndUpdate, nil)
		n.waitForHold(hold)

		// Release only after Close cancels the check; otherwise the small set can finish before Close runs.
		consensus.AfterPruningPointUTXOSetScanStopForTest(n.node, hold.Release)
		closed := make(chan struct{})
		go func() {
			n.node.Close()
			close(closed)
		}()
		select {
		case <-closed:
		case <-time.After(scanTestTimeout):
			t.Fatalf("Close did not return while the pruning point UTXO set check ran")
		}
		select {
		case <-hold.closed:
		default:
			t.Fatalf("Close returned before the check closed its cursor")
		}
		if !n.updating() || n.blockDeleted(firstPruningPoint) {
			t.Fatalf("the stopped check finished the update")
		}

		// Keep the restarted check pending until the refusal below is observed.
		restartHold := n.db.armHold()
		defer restartHold.Release()
		started := n.db.started
		node, _, err := consensus.NewFactory().NewConsensus(n.nodeConfig, n.db, n.dbPrefix, nil)
		if err != nil {
			t.Fatalf("NewConsensus on restart: %+v", err)
		}
		n.node = node
		if n.db.started != started+1 {
			t.Fatalf("the restart ran %d pruning point UTXO set updates, want 1", n.db.started-started)
		}
		n.waitForHold(restartHold)
		n.requireRefused(pruningPoint)
		restartHold.Release()
		n.waitUntilUpdated()
		n.requireServedAndVerified(pruningPoint)
		if !n.blockDeleted(firstPruningPoint) {
			t.Fatalf("the finished update did not delete the blocks of the previous pruning point")
		}
		n.requireNoMismatch()
	})
}

// TestPruningPointMoveDuringTheUTXOSetCheck moves the pruning point again while the check of the previous move's set
// runs. That check is superseded: its result is dropped - it must neither stop the node nor finish the update with the
// older set - and the new pruning point's set is checked and finishes the update. The move is seen either through the
// set written again for the new pruning point, or, before that, through the pruning point alone. (The block source of
// these tests cannot mine much past two moves, so the first move's check is the one held.)
func TestPruningPointMoveDuringTheUTXOSetCheck(t *testing.T) {
	for _, newSetWritten := range []bool{true, false} {
		name := "new-set-written"
		if !newSetWritten {
			name = "new-set-not-written-yet"
		}
		t.Run(name, func(t *testing.T) {
			testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
				defer constants.ForceSetBlockVersion(1)
				testPruningPointMoveDuringTheUTXOSetCheck(t, consensusConfig, newSetWritten)
			})
		})
	}
}

func testPruningPointMoveDuringTheUTXOSetCheck(t *testing.T, consensusConfig *consensus.Config, newSetWritten bool) {
	n, teardown := newPruningMoveNode(t, consensusConfig, "TestPruningPointMoveDuringTheUTXOSetCheck", true)
	defer teardown()

	hold := n.db.armHold()
	defer hold.Release()
	heldPruningPoint := n.insertUntilThePruningPointMoves(n.insertAndUpdate, nil)
	n.waitForHold(hold)

	started := n.db.started
	insert := n.insertAndUpdate
	if !newSetWritten {
		insert = func(block *externalapi.DomainBlock) error {
			return n.within("inserting a block", func() error { return n.node.ValidateAndInsertBlock(block, true, true) })
		}
	}
	newPruningPoint := n.insertUntilThePruningPointMoves(insert, nil)
	wantStarted := started
	if newSetWritten {
		wantStarted++
	}
	if n.db.started != wantStarted {
		t.Fatalf("%d pruning point UTXO set updates ran during the check, want %d", n.db.started-started,
			wantStarted-started)
	}
	n.requireRefused(newPruningPoint, heldPruningPoint)

	hold.Release()
	if !newSetWritten {
		// The superseded check ends without the set written for the new pruning point. Nothing may finish with the
		// older set meanwhile.
		select {
		case <-hold.closed:
		case <-time.After(scanTestTimeout):
			t.Fatalf("the superseded check did not end")
		}
		for deadline := time.Now().Add(200 * time.Millisecond); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
			if !n.updating() {
				t.Fatalf("the superseded check finished the update with the previous pruning point's set")
			}
		}
		n.requireNoMismatch()
		err := n.within("UpdatePruningPointIfRequired", n.node.UpdatePruningPointIfRequired)
		if err != nil {
			t.Fatalf("UpdatePruningPointIfRequired: %+v", err)
		}
		if n.db.started != started+1 {
			t.Fatalf("%d pruning point UTXO set updates ran, want 1", n.db.started-started)
		}
	}
	n.waitUntilUpdated()
	n.requireServedAndVerified(newPruningPoint)
	n.requireRefused(heldPruningPoint)
	if !n.blockDeleted(heldPruningPoint) {
		t.Fatalf("the finished update did not delete the blocks below the new pruning point")
	}
	n.requireNoMismatch()
}
