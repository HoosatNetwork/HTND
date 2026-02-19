package testutils

import (
	"os"
	"testing"

	consensusdatabase "github.com/Hoosat-Oy/HTND/domain/consensus/database"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/utxo"
	"github.com/Hoosat-Oy/HTND/infrastructure/db/database/ldb"
)

// NewTestDB creates a temporary LevelDB-backed consensus DBManager and a prefix bucket for stores.
func NewTestDB(t *testing.T) (dbManager model.DBManager, prefixBucket model.DBBucket, teardown func()) {
	t.Helper()

	tmpDir, err := os.MkdirTemp("", "htnd-datastructures-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}

	db, err := ldb.NewLevelDB(tmpDir, 8)
	if err != nil {
		_ = os.RemoveAll(tmpDir)
		t.Fatalf("NewLevelDB: %v", err)
	}

	dbManager = consensusdatabase.New(db)
	prefixBucket = consensusdatabase.MakeBucket([]byte("datastructures-test"))

	teardown = func() {
		_ = db.Close()
		_ = os.RemoveAll(tmpDir)
	}

	return dbManager, prefixBucket, teardown
}

// Commit commits the given staging area inside a DB transaction.
func Commit(t *testing.T, dbManager model.DBManager, stagingArea *model.StagingArea) {
	t.Helper()

	dbTx, err := dbManager.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer dbTx.RollbackUnlessClosed()

	if err := stagingArea.Commit(dbTx); err != nil {
		t.Fatalf("stagingArea.Commit: %v", err)
	}
	if err := dbTx.Commit(); err != nil {
		t.Fatalf("dbTx.Commit: %v", err)
	}
}

// Hash returns a deterministic DomainHash for test i.
// It's intentionally not cryptographically random.
func Hash(i byte) *externalapi.DomainHash {
	var arr [externalapi.DomainHashSize]byte
	for j := range len(arr) {
		arr[j] = i
	}
	// Make it slightly less uniform to catch byte-order issues.
	arr[0] = i
	arr[1] = i + 1
	arr[2] = i + 2
	return externalapi.NewDomainHashFromByteArray(&arr)
}

func TxID(i byte) *externalapi.DomainTransactionID {
	return (*externalapi.DomainTransactionID)(Hash(i))
}

func Outpoint(txIDByte byte, index uint32) *externalapi.DomainOutpoint {
	txID := TxID(txIDByte)
	return externalapi.NewDomainOutpoint(txID, index)
}

func UTXOEntry(amount uint64, daaScore uint64) externalapi.UTXOEntry {
	return utxo.NewUTXOEntry(amount, &externalapi.ScriptPublicKey{Script: []byte{1, 2, 3}, Version: 0}, false, daaScore)
}

func OutpointPair(txIDByte byte, index uint32, amount uint64, daaScore uint64) *externalapi.OutpointAndUTXOEntryPair {
	return &externalapi.OutpointAndUTXOEntryPair{
		Outpoint:  Outpoint(txIDByte, index),
		UTXOEntry: UTXOEntry(amount, daaScore),
	}
}
