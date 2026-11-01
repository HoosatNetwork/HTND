package pruningmanager

import (
	"errors"
	"strings"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/database"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/datastructures/pruningstore"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	rawdb "github.com/HoosatNetwork/HTND/v2/infrastructure/db/database"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database/ldb"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database/pebble"
)

func TestPP42UnsupportedBackendKeepsSynchronousPath(t *testing.T) {
	raw, err := ldb.NewLevelDB(t.TempDir(), 8)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	db := database.New(raw)
	prefix := database.MakeBucket([]byte("instance/"))
	pm := &pruningManager{databaseContext: db, pruningStore: pruningstore.New(prefix, 2, false), requestUTXOSetScan: func() {}}
	if err := ConfigureJournalWorker(pm, prefix, nil, pruningstore.JournalDiskPolicy{}); err != nil {
		t.Fatal(err)
	}
	if enabled, err := ConfigureHistoryJobs(pm); err != nil || enabled {
		t.Fatalf("unsupported backend enabled history jobs: %t %v", enabled, err)
	}
	if JournalActive(pm) || pm.journalRequested {
		t.Fatal("unsupported journal enabled")
	}
	j := pruningstore.NewJournal(db, prefix, pm.pruningStore)
	if state, err := j.Load(); err != nil || state != nil {
		t.Fatalf("admission wrote marker: %v %v", state, err)
	}
	// Existing durable work must never fall back to legacy semantics silently.
	marker := []byte(`{"Version":1,"Phase":"complete","BaseIndex":0,"TargetIndex":0}`)
	if err := db.Put(prefix.Key([]byte("pruning-point-utxo-set-journal-state")), marker); err != nil {
		t.Fatal(err)
	}
	if err := ConfigureJournalWorker(pm, prefix, nil, pruningstore.JournalDiskPolicy{}); !errors.Is(err, database.ErrSnapshotUnsupported) {
		t.Fatalf("durable work on unsupported backend was not refused: %v", err)
	}
}

func TestPP42WALAdmissionPrecedesJournalInitialization(t *testing.T) {
	t.Setenv("HTND_TEST_MODE", "1")
	raw, err := pebble.OpenPebbleDB(t.TempDir(), 8)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	checkNoJournalInitialization(t, raw)
}

func checkNoJournalInitialization(t *testing.T, raw rawdb.Database) {
	t.Helper()
	db := database.New(raw)
	prefix := database.MakeBucket([]byte("instance/"))
	pm := &pruningManager{databaseContext: db, genesisHash: externalapi.NewZeroHash(),
		pruningStore: pruningstore.New(prefix, 2, false), requestUTXOSetScan: func() {}}
	if err := ConfigureJournalWorker(pm, prefix, nil, pruningstore.JournalDiskPolicy{}); err == nil || !strings.Contains(err.Error(), "WAL") {
		t.Fatalf("WAL-disabled initialization accepted: %v", err)
	}
	if JournalActive(pm) {
		t.Fatal("journal installed after failed admission")
	}
	j := pruningstore.NewJournal(db, prefix, pm.pruningStore)
	if state, err := j.Load(); err != nil || state != nil {
		t.Fatalf("admission wrote marker: %v %v", state, err)
	}
	if _, err := pm.pruningStore.CurrentPruningPointIndex(db, model.NewStagingArea()); !database.IsNotFoundError(err) {
		t.Fatalf("admission changed pruning state: %v", err)
	}
}
