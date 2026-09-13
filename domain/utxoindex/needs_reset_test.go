package utxoindex

import (
	"os"
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus/database/binaryserialization"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	consensusutxo "github.com/HoosatNetwork/HTND/domain/consensus/utils/utxo"
	"github.com/HoosatNetwork/HTND/infrastructure/db/database/ldb"
)

// TestDroppedVirtualChangeSetForcesRebuild pins that an index which learns it missed a diff is
// rebuilt on the next start. The virtual parents it stores keep advancing with every later diff, so
// they alone would make the startup check call it synced and the drift would be kept for good.
func TestDroppedVirtualChangeSetForcesRebuild(t *testing.T) {
	path, err := os.MkdirTemp("", "utxoindex-needs-reset")
	if err != nil {
		t.Fatalf("MkdirTemp: %s", err)
	}
	defer os.RemoveAll(path)

	db, err := ldb.NewLevelDB(path, 8)
	if err != nil {
		t.Fatalf("NewLevelDB: %s", err)
	}
	defer db.Close()
	if err := db.Put(circulatingSupplyKey, binaryserialization.SerializeUint64(0)); err != nil {
		t.Fatalf("initializing circulating supply: %s", err)
	}

	// No domain: isSynced must settle this case without asking consensus.
	ui := &UTXOIndex{store: newUTXOIndexStore(db)}
	needsReset := func() bool {
		t.Helper()
		marked, err := ui.store.needsReset()
		if err != nil {
			t.Fatalf("needsReset: %s", err)
		}
		return marked
	}

	_, err = ui.Update(&externalapi.VirtualChangeSet{VirtualUTXODiff: consensusutxo.NewUTXODiff()})
	if err != nil {
		t.Fatalf("Update: %s", err)
	}
	if needsReset() {
		t.Fatalf("an ordinary change set marked the index for a rebuild")
	}

	_, err = ui.Update(&externalapi.VirtualChangeSet{
		VirtualUTXODiff:          consensusutxo.NewUTXODiff(),
		EarlierChangeSetsDropped: true,
	})
	if err != nil {
		t.Fatalf("Update: %s", err)
	}
	if !needsReset() {
		t.Fatalf("a change set reporting a dropped predecessor did not mark the index for a rebuild")
	}

	synced, err := ui.isSynced()
	if err != nil {
		t.Fatalf("isSynced: %s", err)
	}
	if synced {
		t.Fatalf("an index marked for a rebuild reports itself synced")
	}

	if err := ui.store.deleteAll(); err != nil {
		t.Fatalf("deleteAll: %s", err)
	}
	if needsReset() {
		t.Fatalf("a rebuild does not clear the mark, so every later start would rebuild again")
	}
}
