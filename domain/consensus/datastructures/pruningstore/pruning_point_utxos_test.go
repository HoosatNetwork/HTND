package pruningstore

import (
	"testing"

	consensusdatabase "github.com/HoosatNetwork/HTND/domain/consensus/database"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/utxo"
	"github.com/HoosatNetwork/HTND/infrastructure/db/database/ldb"
)

// TestPruningPointUTXOsPaginationIsComplete pins the contract the pruning-point UTXO transfer
// relies on: walking the set in `limit`-sized pages, resuming from the last outpoint of the
// previous page, must yield every entry exactly once.
//
// The transfer sends the set to a syncing peer in 1000-entry chunks and resumes each chunk with
// Seek(lastOutpoint) followed by Next(). An off-by-one either way is invisible on the wire - the
// receiver has no way to know how many entries it should have received - so it is pinned here
// instead.
func TestPruningPointUTXOsPaginationIsComplete(t *testing.T) {
	dataDir := t.TempDir()
	db, err := ldb.NewLevelDB(dataDir, 8)
	if err != nil {
		t.Fatalf("could not open database: %+v", err)
	}
	defer db.Close()

	store := New(consensusdatabase.MakeBucket(nil), 2, false).(*pruningStore)
	dbManager := consensusdatabase.New(db)

	const entryCount = 250
	expected := make(map[externalapi.DomainOutpoint]uint64, entryCount)
	pairs := make([]*externalapi.OutpointAndUTXOEntryPair, 0, entryCount)
	for i := 0; i < entryCount; i++ {
		var idBytes [externalapi.DomainHashSize]byte
		idBytes[0] = byte(i)
		idBytes[1] = byte(i >> 8)
		outpoint := externalapi.DomainOutpoint{
			TransactionID: *externalapi.NewDomainTransactionIDFromByteArray(&idBytes),
			Index:         uint32(i % 3),
		}
		entry := utxo.NewUTXOEntry(uint64(1000+i), &externalapi.ScriptPublicKey{Script: []byte{0x51}}, false, uint64(i))
		expected[outpoint] = entry.Amount()
		outpointCopy := outpoint
		pairs = append(pairs, &externalapi.OutpointAndUTXOEntryPair{Outpoint: &outpointCopy, UTXOEntry: entry})
	}

	dbTx, err := dbManager.Begin()
	if err != nil {
		t.Fatalf("begin: %+v", err)
	}
	if err := store.AppendImportedPruningPointUTXOs(dbTx, pairs); err != nil {
		t.Fatalf("append: %+v", err)
	}
	if err := dbTx.Commit(); err != nil {
		t.Fatalf("commit: %+v", err)
	}
	if err := store.CommitImportedPruningPointUTXOSet(dbManager); err != nil {
		t.Fatalf("promote: %+v", err)
	}

	// Page through exactly as the transfer does.
	for _, limit := range []int{1, 7, 64, 250, 1000} {
		seen := make(map[externalapi.DomainOutpoint]int)
		var fromOutpoint *externalapi.DomainOutpoint
		pages := 0
		for {
			page, err := store.PruningPointUTXOs(dbManager, fromOutpoint, limit)
			if err != nil {
				t.Fatalf("limit %d, page %d: %+v", limit, pages, err)
			}
			for _, pair := range page {
				seen[*pair.Outpoint]++
			}
			pages++
			if len(page) < limit {
				break
			}
			fromOutpoint = page[len(page)-1].Outpoint
			if pages > entryCount+10 {
				t.Fatalf("limit %d: pagination did not terminate", limit)
			}
		}

		if len(seen) != len(expected) {
			t.Errorf("limit %d: saw %d distinct outpoints, want %d", limit, len(seen), len(expected))
		}
		for outpoint := range expected {
			switch seen[outpoint] {
			case 1:
			case 0:
				t.Errorf("limit %d: outpoint %s:%d was never returned - a peer would receive a set "+
					"missing this coin and be told the transfer succeeded",
					limit, outpoint.TransactionID, outpoint.Index)
			default:
				t.Errorf("limit %d: outpoint %s:%d returned %d times",
					limit, outpoint.TransactionID, outpoint.Index, seen[outpoint])
			}
		}
	}
}

// TestPruningPointUTXOsResumeFromVanishedOutpoint documents what happens when the entry a
// transfer is resuming from is deleted underneath it, which is possible because consensus is
// only locked per chunk and a pruning-point advance rewrites the bucket in place.
//
// Whatever the backend does here, it must not be mistaken for "the set ended". The transfer
// handler now returns the error instead of falling through to send an empty final chunk.
func TestPruningPointUTXOsResumeFromVanishedOutpoint(t *testing.T) {
	dataDir := t.TempDir()
	db, err := ldb.NewLevelDB(dataDir, 8)
	if err != nil {
		t.Fatalf("could not open database: %+v", err)
	}
	defer db.Close()

	store := New(consensusdatabase.MakeBucket(nil), 2, false).(*pruningStore)
	dbManager := consensusdatabase.New(db)

	var presentBytes [externalapi.DomainHashSize]byte
	presentBytes[0] = 1
	present := externalapi.DomainOutpoint{
		TransactionID: *externalapi.NewDomainTransactionIDFromByteArray(&presentBytes),
		Index:         0,
	}
	entry := utxo.NewUTXOEntry(5, &externalapi.ScriptPublicKey{Script: []byte{0x51}}, false, 1)

	dbTx, err := dbManager.Begin()
	if err != nil {
		t.Fatalf("begin: %+v", err)
	}
	if err := store.AppendImportedPruningPointUTXOs(dbTx,
		[]*externalapi.OutpointAndUTXOEntryPair{{Outpoint: &present, UTXOEntry: entry}}); err != nil {
		t.Fatalf("append: %+v", err)
	}
	if err := dbTx.Commit(); err != nil {
		t.Fatalf("commit: %+v", err)
	}
	if err := store.CommitImportedPruningPointUTXOSet(dbManager); err != nil {
		t.Fatalf("promote: %+v", err)
	}

	var vanishedBytes [externalapi.DomainHashSize]byte
	vanishedBytes[0] = 9
	vanished := externalapi.DomainOutpoint{
		TransactionID: *externalapi.NewDomainTransactionIDFromByteArray(&vanishedBytes),
		Index:         7,
	}

	page, err := store.PruningPointUTXOs(dbManager, &vanished, 10)
	// Either outcome is acceptable at this layer; what matters is that an error is an error and
	// is not silently turned into "the set is finished" by the caller.
	if err == nil {
		t.Logf("backend resumed past a vanished outpoint, returning %d entries", len(page))
	} else {
		t.Logf("backend reported an error for a vanished resume point: %v", err)
	}
}
