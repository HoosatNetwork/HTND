package utxoindex

import (
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/database/binaryserialization"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	consensusutxo "github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/utxo"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database/ldb"
	"github.com/HoosatNetwork/HTND/v2/util/memory"
)

// TestPaginatedUTXOsOffset pins that offset counts the UTXOs to skip. Entries used to be collected only once the
// position was strictly greater than offset, so offset 0 never returned an address's first UTXO and paging with
// offset += limit dropped one UTXO at every page boundary. Since offset is unsigned, no request could reach the first
// UTXO at all, and wallets using the paginated RPC under-reported balances.
func TestPaginatedUTXOsOffset(t *testing.T) {
	db, err := ldb.NewLevelDB(t.TempDir(), 8)
	if err != nil {
		t.Fatalf("NewLevelDB: %s", err)
	}
	defer db.Close()

	store := newUTXOIndexStore(db)
	if err := db.Put(circulatingSupplyKey, binaryserialization.SerializeUint64(0)); err != nil {
		t.Fatalf("initializing circulating supply: %s", err)
	}
	scriptPublicKey := &externalapi.ScriptPublicKey{Script: []byte{0x51, 0x21, 0x02}, Version: 0}

	const utxoCount = 5
	for i := range utxoCount {
		outpoint := &externalapi.DomainOutpoint{
			TransactionID: *externalapi.NewDomainTransactionIDFromByteArray(&[32]byte{byte(i + 1)}),
		}
		// Distinct amounts identify each coin in the results.
		entry := consensusutxo.NewUTXOEntry(uint64(1000+i), scriptPublicKey, false, 100)
		if err := store.add(scriptPublicKey, outpoint, entry); err != nil {
			t.Fatalf("add: %s", err)
		}
	}
	if err := store.commit(); err != nil {
		t.Fatalf("commit: %s", err)
	}

	page := func(offset, limit uint32) []uint64 {
		buffer := memory.Malloc[UTXOPair](1)
		pairs, buffer, err := store.PaginatedUTXOs(scriptPublicKey, offset, limit, buffer)
		defer memory.Free(buffer)
		if err != nil {
			t.Fatalf("PaginatedUTXOs(offset %d, limit %d): %s", offset, limit, err)
		}
		amounts := make([]uint64, len(pairs))
		for i, pair := range pairs {
			amounts[i] = pair.Entry.Amount()
		}
		return amounts
	}

	if all := page(0, 0); len(all) != utxoCount {
		t.Fatalf("offset 0 without a limit returned %d UTXOs, want all %d", len(all), utxoCount)
	}

	// Walking pages of two with offset += limit must visit every coin exactly once.
	seen := make(map[uint64]int)
	for offset := uint32(0); offset < utxoCount; offset += 2 {
		for _, amount := range page(offset, 2) {
			seen[amount]++
		}
	}
	if len(seen) != utxoCount {
		t.Fatalf("paging with offset += limit visited %d distinct UTXOs, want %d (%v)", len(seen), utxoCount, seen)
	}
	for amount, count := range seen {
		if count != 1 {
			t.Fatalf("UTXO with amount %d was returned %d times", amount, count)
		}
	}

	if rest := page(utxoCount-1, 0); len(rest) != 1 {
		t.Fatalf("offset %d without a limit returned %d UTXOs, want 1", utxoCount-1, len(rest))
	}
	if beyond := page(utxoCount, 0); len(beyond) != 0 {
		t.Fatalf("offset past the end returned %d UTXOs, want 0", len(beyond))
	}
}
