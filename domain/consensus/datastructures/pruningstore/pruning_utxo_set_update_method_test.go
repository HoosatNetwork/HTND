package pruningstore

import (
	"testing"

	consensusdatabase "github.com/HoosatNetwork/HTND/domain/consensus/database"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/infrastructure/db/database/ldb"
)

// TestPruningPointUTXOSetUpdateMethodBelongsToItsPruningPoint pins the contract interrupted pruning point UTXO set
// updates resume on: the recorded diff method is returned only for the pruning point it was recorded for, and
// finishing the update clears it, so a record left behind by an earlier update is never reused for a later one.
func TestPruningPointUTXOSetUpdateMethodBelongsToItsPruningPoint(t *testing.T) {
	db, err := ldb.NewLevelDB(t.TempDir(), 8)
	if err != nil {
		t.Fatalf("NewLevelDB: %+v", err)
	}
	defer db.Close()
	dbManager := consensusdatabase.New(db)
	store := New(consensusdatabase.MakeBucket(nil), 2, false)

	pruningPoint := externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{1})
	laterPruningPoint := externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{2})

	if _, found, err := store.PruningPointUTXOSetUpdateMethod(dbManager, pruningPoint); err != nil || found {
		t.Fatalf("a method is recorded before any update (found %t, err %v)", found, err)
	}
	if err := store.StorePruningPointUTXOSetUpdateMethod(dbManager, pruningPoint, "diff-chain-walk"); err != nil {
		t.Fatalf("StorePruningPointUTXOSetUpdateMethod: %+v", err)
	}
	method, found, err := store.PruningPointUTXOSetUpdateMethod(dbManager, pruningPoint)
	if err != nil || !found || method != "diff-chain-walk" {
		t.Fatalf("recorded method for its own pruning point: got %q, found %t, err %v", method, found, err)
	}
	if _, found, err := store.PruningPointUTXOSetUpdateMethod(dbManager, laterPruningPoint); err != nil || found {
		t.Fatalf("a method recorded for one pruning point was returned for another (found %t, err %v)", found, err)
	}
	if err := store.FinishUpdatingPruningPointUTXOSet(dbManager); err != nil {
		t.Fatalf("FinishUpdatingPruningPointUTXOSet: %+v", err)
	}
	if _, found, err := store.PruningPointUTXOSetUpdateMethod(dbManager, pruningPoint); err != nil || found {
		t.Fatalf("the recorded method survived finishing the update (found %t, err %v)", found, err)
	}
}
