package flowcontext

import (
	"bytes"
	"math"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/domain"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/testutils"
	"github.com/HoosatNetwork/HTND/v2/domain/miningmanager/mempool"
	"github.com/HoosatNetwork/HTND/v2/domain/prefixmanager/prefix"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database/ldb"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter"
	"github.com/pkg/errors"
)

var errInjected = errors.New("injected failure of the pruning point UTXO set update")

// failingUpdateDB fails, when armed, one write of a pruning point UTXO set update: the record of its diff method,
// which the update stores before it changes anything; the sixth entry it writes into the set, leaving the set half
// written; or the commit that deletes past blocks - the first transaction commit while the update is durably pending
// - which comes after the whole set was written.
type failingUpdateDB struct {
	database.Database
	updatingKey  *database.Key
	failMethod   bool
	failSetWrite bool
	setWrites    int
	// failDeletion is used from the background check of the pruning point UTXO set, which makes the deletion commit.
	failDeletion atomic.Bool
}

func (db *failingUpdateDB) Put(key *database.Key, value []byte) error {
	if db.failMethod && string(key.Suffix()) == "pruning-point-utxo-set-update-method" {
		db.failMethod = false
		return errInjected
	}
	if db.failSetWrite && bytes.Contains(key.Bucket().Path(), []byte("pruning-point-utxo-set")) {
		db.setWrites++
		if db.setWrites == 6 {
			db.failSetWrite = false
			return errInjected
		}
	}
	return db.Database.Put(key, value)
}

func (db *failingUpdateDB) Begin() (database.Transaction, error) {
	tx, err := db.Database.Begin()
	return &failingUpdateTransaction{Transaction: tx, db: db}, err
}

func (db *failingUpdateDB) BeginUnindexed() (database.Transaction, error) {
	tx, err := db.Database.BeginUnindexed()
	return &failingUpdateTransaction{Transaction: tx, db: db}, err
}

type failingUpdateTransaction struct {
	database.Transaction
	db *failingUpdateDB
}

func (tx *failingUpdateTransaction) Commit() error {
	if tx.db.failDeletion.Load() {
		updating, err := tx.db.Database.Has(tx.db.updatingKey)
		if err != nil {
			return err
		}
		if updating {
			tx.db.failDeletion.Store(false)
			_ = tx.Transaction.Rollback()
			return errInjected
		}
	}
	return tx.Transaction.Commit()
}

// TestAddBlockKeepsAFailedPruningPointUpdatePending pins how a block submitted through AddBlock treats the pruning
// point UTXO set update it leaves pending, with DeferPruningPointUTXOSetUpdate on as the node's app sets it. If the
// update fails before its last commit, AddBlock still reports the block as added - it is committed - and the update
// stays pending. The next AddBlock runs it again and finishes it. The last commit is made by the update's check of the
// set against its commitment, which runs in the background.
func TestAddBlockKeepsAFailedPruningPointUpdatePending(t *testing.T) {
	for _, failure := range []string{"diff method record", "pruning point UTXO set write", "past block deletion commit"} {
		t.Run(failure, func(t *testing.T) {
			testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
				defer constants.ForceSetBlockVersion(1)
				testAddBlockKeepsAFailedPruningPointUpdatePending(t, consensusConfig, failure)
			})
		})
	}
}

func testAddBlockKeepsAFailedPruningPointUpdatePending(t *testing.T, consensusConfig *consensus.Config, failure string) {
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
	consensusConfig.DeferPruningPointUTXOSetUpdate = true

	db, err := ldb.NewLevelDB(t.TempDir(), 8)
	if err != nil {
		t.Fatalf("NewLevelDB: %+v", err)
	}
	defer db.Close()
	updatingKey := database.MakeBucket((&prefix.Prefix{}).Serialize()).Key([]byte("updating-pruning-point-utxo-set"))
	failingDB := &failingUpdateDB{Database: db, updatingKey: updatingKey}
	domainInstance, err := domain.New(consensusConfig, mempool.DefaultConfig(&consensusConfig.Params), failingDB)
	if err != nil {
		t.Fatalf("domain.New: %+v", err)
	}
	defer domainInstance.Consensus().Close()
	flowContext := New(nil, domainInstance, nil, &netadapter.NetAdapter{}, nil)
	flowContext.lastRebroadcastTime = time.Now()
	updating := func() bool {
		updating, err := db.Has(updatingKey)
		if err != nil {
			t.Fatalf("Has: %+v", err)
		}
		return updating
	}
	scriptPublicKey, _ := testutils.OpTrueScript()
	coinbaseData := &externalapi.DomainCoinbaseData{ScriptPublicKey: scriptPublicKey, ExtraData: []byte{}}
	addBlock := func() {
		block, err := domainInstance.Consensus().BuildBlock(coinbaseData, nil)
		if err != nil {
			t.Fatalf("BuildBlock: %+v", err)
		}
		err = flowContext.AddBlock(asReceived(block))
		if err != nil {
			t.Fatalf("AddBlock: %+v", err)
		}
	}

	switch failure {
	case "diff method record":
		failingDB.failMethod = true
	case "pruning point UTXO set write":
		failingDB.failSetWrite = true
	case "past block deletion commit":
		failingDB.failDeletion.Store(true)
	}
	var pruningPoint *externalapi.DomainHash
	for i := 0; i < 300; i++ {
		addBlock()
		pruningPoint, err = domainInstance.Consensus().PruningPoint()
		if err != nil {
			t.Fatalf("PruningPoint: %+v", err)
		}
		if !pruningPoint.Equal(consensusConfig.GenesisHash) {
			break
		}
	}
	if pruningPoint.Equal(consensusConfig.GenesisHash) {
		t.Fatalf("setup: the pruning point never moved")
	}
	waitFor := func(what string, condition func() bool) {
		for deadline := time.Now().Add(30 * time.Second); !condition(); time.Sleep(time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting until %s", what)
			}
		}
	}
	waitFor("the injected failure happened", func() bool { return !failingDB.failDeletion.Load() })
	if failingDB.failMethod || failingDB.failSetWrite {
		t.Fatalf("setup: the injected failure never happened")
	}
	if !updating() {
		t.Fatalf("the failed pruning point UTXO set update is no longer pending")
	}

	addBlock()
	waitFor("the next AddBlock finished the pending pruning point UTXO set update", func() bool { return !updating() })
	health, err := domainInstance.Consensus().CheckUTXOHealth(pruningPoint)
	if err != nil {
		t.Fatalf("CheckUTXOHealth: %+v", err)
	}
	if !health.Verified {
		t.Errorf("the served pruning point UTXO set does not match the pruning point's commitment: %+v", health)
	}
}
