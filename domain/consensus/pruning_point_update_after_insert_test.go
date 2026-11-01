package consensus_test

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/testapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/ruleerrors"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/testutils"
	"github.com/HoosatNetwork/HTND/v2/domain/prefixmanager/prefix"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database/ldb"
)

const updatingPruningPointUTXOSetKeyName = "updating-pruning-point-utxo-set"

// updateCountingDB counts the pruning point UTXO set updates that ran: each one first records its diff method. With a
// scanHold armed, it also holds the next cursor opened on the pruning point UTXO set - the background check's, since
// nothing else opens one there while an update is pending - at its first step.
type updateCountingDB struct {
	database.Database
	started int

	holdMu sync.Mutex
	hold   *scanHold
}

func (db *updateCountingDB) Put(key *database.Key, value []byte) error {
	if string(key.Suffix()) == "pruning-point-utxo-set-update-method" {
		db.started++
	}
	return db.Database.Put(key, value)
}

// scanHold holds a cursor at its first step. reached is closed when the cursor gets there, closed when it is closed;
// release lets it go on.
type scanHold struct {
	reached, closed chan struct{}
	release         chan struct{}
	reachedOnce     sync.Once
	closedOnce      sync.Once
	releaseOnce     sync.Once
}

func (h *scanHold) Release() {
	h.releaseOnce.Do(func() { close(h.release) })
}

// armHold makes the next cursor opened on the pruning point UTXO set wait at its first step until released.
func (db *updateCountingDB) armHold() *scanHold {
	db.holdMu.Lock()
	defer db.holdMu.Unlock()
	db.hold = &scanHold{reached: make(chan struct{}), closed: make(chan struct{}), release: make(chan struct{})}
	return db.hold
}

func (db *updateCountingDB) Cursor(bucket *database.Bucket) (database.Cursor, error) {
	cursor, err := db.Database.Cursor(bucket)
	if err != nil || !bytes.HasSuffix(bucket.Path(), []byte("/pruning-point-utxo-set/")) {
		return cursor, err
	}
	db.holdMu.Lock()
	defer db.holdMu.Unlock()
	hold := db.hold
	db.hold = nil
	if hold == nil {
		return cursor, nil
	}
	return &heldCursor{Cursor: cursor, hold: hold}, nil
}

type heldCursor struct {
	database.Cursor
	hold *scanHold
}

func (c *heldCursor) wait() {
	c.hold.reachedOnce.Do(func() {
		close(c.hold.reached)
		<-c.hold.release
	})
}

func (c *heldCursor) First() bool {
	c.wait()
	return c.Cursor.First()
}

func (c *heldCursor) Next() bool {
	c.wait()
	return c.Cursor.Next()
}

func (c *heldCursor) Close() error {
	defer c.hold.closedOnce.Do(func() { close(c.hold.closed) })
	return c.Cursor.Close()
}

// pruningMoveNode is a node fed with the blocks of a block source, under the shrunk parameters of the pruning tests,
// so that the pruning point moves every few dozen blocks.
type pruningMoveNode struct {
	t           *testing.T
	genesis     *externalapi.DomainHash
	source      testapi.TestConsensus
	node        externalapi.Consensus
	nodeConfig  *consensus.Config
	db          *updateCountingDB
	dbPrefix    *prefix.Prefix
	updatingKey *database.Key
	tip         *externalapi.DomainHash
	// mismatches receives what Config.OnPruningPointUTXOSetMismatch is called with.
	mismatches chan error
}

func newPruningMoveNode(t *testing.T, consensusConfig *consensus.Config, name string, deferUpdate bool) (
	*pruningMoveNode, func(),
) {
	db, err := ldb.NewLevelDB(t.TempDir(), 8)
	if err != nil {
		t.Fatalf("NewLevelDB: %+v", err)
	}
	return newPruningMoveNodeWithDatabase(t, consensusConfig, name, deferUpdate, db)
}

func newPruningMoveNodeWithDatabase(t *testing.T, consensusConfig *consensus.Config, name string,
	deferUpdate bool, db database.Database,
) (*pruningMoveNode, func()) {
	consensusConfig.POWScores = []uint64{math.MaxUint64}
	consensusConfig.K = append([]externalapi.KType(nil), consensusConfig.K...)
	consensusConfig.K[0] = 0
	consensusConfig.FinalityDuration = []time.Duration{30 * consensusConfig.TargetTimePerBlock[0]}
	consensusConfig.PruningProofM = 1
	window := make([]int, len(consensusConfig.DifficultyAdjustmentWindowSize))
	for i := range window {
		window[i] = 10
	}
	consensusConfig.DifficultyAdjustmentWindowSize = window

	factory := consensus.NewFactory()
	source, teardownSource, err := factory.NewTestConsensus(consensusConfig, name)
	if err != nil {
		t.Fatalf("NewTestConsensus: %+v", err)
	}
	dbPrefix := &prefix.Prefix{}
	nodeConfig := *consensusConfig
	nodeConfig.DeferPruningPointUTXOSetUpdate = deferUpdate
	n := &pruningMoveNode{
		t:           t,
		genesis:     consensusConfig.GenesisHash,
		source:      source,
		nodeConfig:  &nodeConfig,
		db:          &updateCountingDB{Database: db},
		dbPrefix:    dbPrefix,
		updatingKey: database.MakeBucket(dbPrefix.Serialize()).Key([]byte(updatingPruningPointUTXOSetKeyName)),
		tip:         consensusConfig.GenesisHash,
		mismatches:  make(chan error, 10),
	}
	nodeConfig.OnPruningPointUTXOSetMismatch = func(err error) { n.mismatches <- err }
	teardown := func() {
		if n.node != nil {
			n.node.Close()
		}
		_ = db.Close()
		teardownSource(false)
	}
	n.node, _, err = factory.NewConsensus(&nodeConfig, n.db, dbPrefix, nil)
	if err != nil {
		teardown()
		t.Fatalf("NewConsensus: %+v", err)
	}
	return n, teardown
}

// restart stops the node and starts it again on the same database.
func (n *pruningMoveNode) restart() {
	n.node.Close()
	node, _, err := consensus.NewFactory().NewConsensus(n.nodeConfig, n.db, n.dbPrefix, nil)
	if err != nil {
		n.node = nil
		n.t.Fatalf("NewConsensus on restart: %+v", err)
	}
	n.node = node
}

// waitUntilUpdated waits for the pending pruning point UTXO set update to finish, and for the history deletion its
// finish recorded: with DeferPruningPointUTXOSetUpdate, both may run in the background.
func (n *pruningMoveNode) waitUntilUpdated() {
	n.t.Helper()
	waitUntil(n.t, "the pending pruning point UTXO set update finished", func() bool {
		return !n.updating() && !historyDeletionPending(n.t, n.db, n.dbPrefix)
	})
}

// historyDeletionPending reports whether a history deletion job is still recorded.
func historyDeletionPending(t *testing.T, db database.DataAccessor, dbPrefix *prefix.Prefix) bool {
	t.Helper()
	cursor, err := db.Cursor(database.MakeBucket(dbPrefix.Serialize()).Bucket([]byte("history-deletion-jobs")))
	if err != nil {
		t.Fatalf("Cursor: %+v", err)
	}
	defer cursor.Close()
	return cursor.First()
}

func waitUntil(t *testing.T, what string, condition func() bool) {
	t.Helper()
	for deadline := time.Now().Add(30 * time.Second); !condition(); {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting until %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// nextBlock mines a block on the source's chain tip.
func (n *pruningMoveNode) nextBlock() *externalapi.DomainBlock {
	hash, _, err := n.source.AddBlock([]*externalapi.DomainHash{n.tip}, nil, nil)
	if err != nil {
		n.t.Fatalf("AddBlock: %+v", err)
	}
	block, found, err := n.source.GetBlock(hash)
	if err != nil || !found {
		n.t.Fatalf("GetBlock from the source: found %t, %+v", found, err)
	}
	n.tip = hash
	return block
}

func (n *pruningMoveNode) pruningPoint() *externalapi.DomainHash {
	pruningPoint, err := n.node.PruningPoint()
	if err != nil {
		n.t.Fatalf("PruningPoint: %+v", err)
	}
	return pruningPoint
}

func (n *pruningMoveNode) updating() bool {
	updating, err := n.db.Has(n.updatingKey)
	if err != nil {
		n.t.Fatalf("Has: %+v", err)
	}
	return updating
}

// insertUntilThePruningPointMoves inserts blocks until one moves the pruning point, and returns the new one. Before
// each insertion it runs check, if given.
func (n *pruningMoveNode) insertUntilThePruningPointMoves(insert func(*externalapi.DomainBlock) error, check func()) *externalapi.DomainHash {
	previous := n.pruningPoint()
	for i := 0; i < 300; i++ {
		if check != nil {
			check()
		}
		err := insert(n.nextBlock())
		if err != nil {
			n.t.Fatalf("inserting block #%d: %+v", i, err)
		}
		if pruningPoint := n.pruningPoint(); !pruningPoint.Equal(previous) {
			return pruningPoint
		}
	}
	n.t.Fatalf("setup: the pruning point never moved")
	return nil
}

// TestPruningPointMoveReturnsBeforeItsUTXOSetUpdate pins the order a pruning point move happens in, on a node with
// DeferPruningPointUTXOSetUpdate on, as the node's app sets it. Inserting the block that moves the pruning point
// returns - so the caller can announce it - while the pruning point UTXO set update is still pending, with the block
// already committed for peers to fetch. Insertions that do not move the pruning point leave it pending, and so do
// requests for the pruning point UTXO set, which are refused until it is done. The next move finishes it first, so the
// following update starts from a set that matches its predecessor. UpdatePruningPointIfRequired, which callers run
// after announcing, runs a pending update, and so does IterateUTXOSetAtBlockFromAcceptanceData, which refuses to read
// the set until the update's check of it against the commitment, run in the background, has finished it.
func TestPruningPointMoveReturnsBeforeItsUTXOSetUpdate(t *testing.T) {
	for _, finisher := range []string{"UpdatePruningPointIfRequired", "IterateUTXOSetAtBlockFromAcceptanceData"} {
		t.Run(finisher, func(t *testing.T) {
			testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
				defer constants.ForceSetBlockVersion(1)
				testPruningPointMoveReturnsBeforeItsUTXOSetUpdate(t, consensusConfig, finisher)
			})
		})
	}
}

func testPruningPointMoveReturnsBeforeItsUTXOSetUpdate(t *testing.T, consensusConfig *consensus.Config, finisher string) {
	n, teardown := newPruningMoveNode(t, consensusConfig, "TestPruningPointMoveReturnsBeforeItsUTXOSetUpdate", true)
	defer teardown()
	insert := func(block *externalapi.DomainBlock) error { return n.node.ValidateAndInsertBlock(block, true, true) }

	firstPruningPoint := n.insertUntilThePruningPointMoves(insert, nil)
	if !n.updating() {
		t.Fatalf("the insertion that moved the pruning point returned only after the UTXO set update, " +
			"so the block could not be relayed until that update finished")
	}
	if _, found, err := n.node.GetBlock(n.tip); err != nil || !found {
		t.Fatalf("the block that moved the pruning point is not readable for peers: found %t, %+v", found, err)
	}

	// Keep the update pending, as if the moving block's flow never got to its call, until the pruning point moves
	// again. Counted from here: the node's startup already ran the update that adding genesis started.
	n.db.started = 0
	pruningPoint := n.insertUntilThePruningPointMoves(insert, func() {
		if n.db.started != 0 {
			t.Fatalf("an insertion that did not move the pruning point started the pending update")
		}
	})
	if n.db.started != 1 {
		t.Fatalf("the next pruning point move ran %d pending updates, want 1: the previous one, "+
			"so its own update starts from its predecessor's set", n.db.started)
	}
	if !n.updating() {
		t.Fatalf("the update of the second pruning point move is not pending")
	}

	// A peer asking for the set - of the new pruning point or an earlier one - is refused and does not start the
	// update.
	started := n.db.started
	for _, requested := range []*externalapi.DomainHash{pruningPoint, firstPruningPoint} {
		_, err := n.node.GetPruningPointUTXOs(requested, nil, 1)
		if !errors.Is(err, ruleerrors.ErrWrongPruningPointHash) {
			t.Fatalf("GetPruningPointUTXOs(%s) while the update is pending: got %v, want ErrWrongPruningPointHash",
				requested, err)
		}
	}
	if n.db.started != started || !n.updating() {
		t.Fatalf("GetPruningPointUTXOs started the pending update")
	}

	iterate := func() error {
		return n.node.IterateUTXOSetAtBlockFromAcceptanceData(pruningPoint,
			func(*externalapi.DomainOutpoint, externalapi.UTXOEntry) error { return nil })
	}
	switch finisher {
	case "UpdatePruningPointIfRequired":
		err := n.node.UpdatePruningPointIfRequired()
		if err != nil {
			t.Fatalf("%s: %+v", finisher, err)
		}
	case "IterateUTXOSetAtBlockFromAcceptanceData":
		// It holds the lock from the update it runs to its refusal, so the background check cannot finish between.
		if err := iterate(); err == nil {
			t.Fatalf("%s read the pruning point UTXO set before its check against the commitment finished", finisher)
		}
	}
	n.waitUntilUpdated()
	if finisher == "IterateUTXOSetAtBlockFromAcceptanceData" {
		if err := iterate(); err != nil {
			t.Fatalf("%s after the update finished: %+v", finisher, err)
		}
	}
	// The caller's own call after announcing then finds nothing to do.
	err := n.node.UpdatePruningPointIfRequired()
	if err != nil {
		t.Fatalf("UpdatePruningPointIfRequired: %+v", err)
	}
	if n.db.started != 2 {
		t.Errorf("%d updates ran, want 2", n.db.started)
	}
	pairs, err := n.node.GetPruningPointUTXOs(pruningPoint, nil, 1000)
	if err != nil || len(pairs) == 0 {
		t.Fatalf("GetPruningPointUTXOs after the update: %d entries, %v", len(pairs), err)
	}

	health, err := n.node.CheckUTXOHealth(pruningPoint)
	if err != nil {
		t.Fatalf("CheckUTXOHealth: %+v", err)
	}
	if !health.Verified {
		t.Errorf("the served pruning point UTXO set does not match the pruning point's commitment: %+v", health)
	}
}

// TestTrustedDataInsertionFinishesThePruningPointUpdate pins that a trusted-data insertion that moves the pruning point
// still runs the update before it returns, with or without DeferPruningPointUTXOSetUpdate: nothing announces such a
// block, so no caller would run it. With DeferPruningPointUTXOSetUpdate its last step runs in the background.
func TestTrustedDataInsertionFinishesThePruningPointUpdate(t *testing.T) {
	for _, deferUpdate := range []bool{false, true} {
		t.Run(fmt.Sprintf("DeferPruningPointUTXOSetUpdate=%t", deferUpdate), func(t *testing.T) {
			testTrustedDataInsertionFinishesThePruningPointUpdate(t, deferUpdate)
		})
	}
}

func testTrustedDataInsertionFinishesThePruningPointUpdate(t *testing.T, deferUpdate bool) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		defer constants.ForceSetBlockVersion(1)
		n, teardown := newPruningMoveNode(t, consensusConfig, "TestTrustedDataInsertionFinishesThePruningPointUpdate", deferUpdate)
		defer teardown()

		n.insertUntilThePruningPointMoves(func(block *externalapi.DomainBlock) error {
			return n.node.ValidateAndInsertBlockWithTrustedData(trustedDataOf(t, n.source, n.tip), true)
		}, nil)
		if !deferUpdate && n.updating() {
			t.Fatalf("DeferPruningPointUTXOSetUpdate=%t: the trusted-data insertion that moved the pruning point "+
				"left its UTXO set update pending", deferUpdate)
		}
		n.waitUntilUpdated()
	})
}

func trustedDataOf(t *testing.T, source testapi.TestConsensus, blockHash *externalapi.DomainHash) *externalapi.BlockWithTrustedData {
	block, _, err := source.GetBlock(blockHash)
	if err != nil {
		t.Fatalf("GetBlock: %+v", err)
	}
	daaWindowHashes, err := source.BlockDAAWindowHashes(blockHash)
	if err != nil {
		t.Fatalf("BlockDAAWindowHashes: %+v", err)
	}
	ghostdagDataHashes, err := source.TrustedBlockAssociatedGHOSTDAGDataBlockHashes(blockHash)
	if err != nil {
		t.Fatalf("TrustedBlockAssociatedGHOSTDAGDataBlockHashes: %+v", err)
	}
	result := &externalapi.BlockWithTrustedData{Block: block}
	for i, daaBlockHash := range daaWindowHashes {
		header, err := source.TrustedDataDataDAAHeader(blockHash, daaBlockHash, uint64(i))
		if err != nil {
			t.Fatalf("TrustedDataDataDAAHeader: %+v", err)
		}
		result.DAAWindow = append(result.DAAWindow, header)
	}
	for _, ghostdagDataHash := range ghostdagDataHashes {
		data, err := source.TrustedGHOSTDAGData(ghostdagDataHash)
		if err != nil {
			t.Fatalf("TrustedGHOSTDAGData: %+v", err)
		}
		result.GHOSTDAGData = append(result.GHOSTDAGData,
			&externalapi.BlockGHOSTDAGDataHashPair{Hash: ghostdagDataHash, GHOSTDAGData: data})
	}
	return result
}
