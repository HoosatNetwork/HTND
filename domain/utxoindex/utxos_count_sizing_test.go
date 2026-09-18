package utxoindex

import (
	"os"
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus/database/binaryserialization"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	consensusutxo "github.com/HoosatNetwork/HTND/domain/consensus/utils/utxo"
	"github.com/HoosatNetwork/HTND/infrastructure/db/database/ldb"
	"github.com/HoosatNetwork/HTND/util/memory"
)

// TestUTXOsSizesFromTheMaintainedCountNotAScan is HTN-214: UTXOs used to size its buffer with a
// dedicated cursor scan that only counted entries, immediately followed by the real fill scan over
// the same cursor - doubling the cost of every unlimited (limit=0) call. applyUTXOCountDeltas already
// maintains an exact per-script count in the same commit as the entries themselves, specifically so a
// caller that needs the count doesn't have to scan for it; UTXOs just wasn't using it.
//
// This adds and removes across several commits - including a net-zero add-then-remove of the same
// outpoint within one script, and outpoints on a second script to rule out cross-script leakage - and
// checks that UTXOs(sp, 0, ...) returns exactly the surviving set, proving the maintained count
// UTXOs now reads stays correct through churn rather than just matching a simple all-adds case.
func TestUTXOsSizesFromTheMaintainedCountNotAScan(t *testing.T) {
	path, err := os.MkdirTemp("", "utxoindex-store")
	if err != nil {
		t.Fatalf("MkdirTemp: %s", err)
	}
	defer os.RemoveAll(path)

	db, err := ldb.NewLevelDB(path, 8)
	if err != nil {
		t.Fatalf("NewLevelDB: %s", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Fatalf("Close: %s", err)
		}
	}()

	store := newUTXOIndexStore(db)
	if err := db.Put(circulatingSupplyKey, binaryserialization.SerializeUint64(0)); err != nil {
		t.Fatalf("initializing circulating supply: %s", err)
	}

	scriptA := &externalapi.ScriptPublicKey{Script: []byte{0x51, 0x21, 0x0a}, Version: 0}
	scriptB := &externalapi.ScriptPublicKey{Script: []byte{0x51, 0x21, 0x0b}, Version: 0}
	entry := consensusutxo.NewUTXOEntry(1000, scriptA, false, 100)

	outpoint := func(b byte, index uint32) *externalapi.DomainOutpoint {
		return &externalapi.DomainOutpoint{
			TransactionID: *externalapi.NewDomainTransactionIDFromByteArray(&[32]byte{b}),
			Index:         index,
		}
	}

	// Commit 1: 5 outpoints on A, 2 on B.
	const firstBatch = 5
	for i := byte(0); i < firstBatch; i++ {
		if err := store.add(scriptA, outpoint(i, 0), entry); err != nil {
			t.Fatalf("add scriptA[%d]: %s", i, err)
		}
	}
	for i := byte(0); i < 2; i++ {
		if err := store.add(scriptB, outpoint(100+i, 0), entry); err != nil {
			t.Fatalf("add scriptB[%d]: %s", i, err)
		}
	}
	if err := store.commit(); err != nil {
		t.Fatalf("commit 1: %s", err)
	}

	// Commit 2: add-then-remove the same outpoint within one commit (nets to zero), plus 3 more real
	// additions, plus removing one from the first batch.
	netZero := outpoint(200, 0)
	if err := store.add(scriptA, netZero, entry); err != nil {
		t.Fatalf("add net-zero outpoint: %s", err)
	}
	if err := store.remove(scriptA, netZero, entry); err != nil {
		t.Fatalf("remove net-zero outpoint: %s", err)
	}
	const secondBatch = 3
	for i := byte(0); i < secondBatch; i++ {
		if err := store.add(scriptA, outpoint(50+i, 0), entry); err != nil {
			t.Fatalf("add scriptA second batch[%d]: %s", i, err)
		}
	}
	if err := store.remove(scriptA, outpoint(0, 0), entry); err != nil {
		t.Fatalf("remove scriptA[0]: %s", err)
	}
	if err := store.commit(); err != nil {
		t.Fatalf("commit 2: %s", err)
	}

	// Surviving on A: firstBatch(5) - 1 removed + secondBatch(3) = 7. The net-zero outpoint must not
	// appear at all, and B's count must be unaffected by any of A's churn.
	const wantA = firstBatch - 1 + secondBatch
	pairsA, bufA, err := store.UTXOs(scriptA, 0, nil)
	if err != nil {
		t.Fatalf("UTXOs scriptA: %s", err)
	}
	defer memory.Free(bufA)
	if len(pairsA) != wantA {
		t.Fatalf("expected %d UTXOs for scriptA, got %d: %+v", wantA, len(pairsA), pairsA)
	}
	for _, pair := range pairsA {
		if pair.Outpoint.Equal(netZero) {
			t.Fatalf("net-zero outpoint %s must not appear in scriptA's UTXOs", netZero)
		}
	}

	pairsB, bufB, err := store.UTXOs(scriptB, 0, nil)
	if err != nil {
		t.Fatalf("UTXOs scriptB: %s", err)
	}
	defer memory.Free(bufB)
	if len(pairsB) != 2 {
		t.Fatalf("expected 2 UTXOs for scriptB (unaffected by scriptA's churn), got %d", len(pairsB))
	}

	// limit>0 must still truncate correctly and independently of the maintained count.
	limited, bufLimited, err := store.UTXOs(scriptA, 3, nil)
	if err != nil {
		t.Fatalf("UTXOs scriptA with limit=3: %s", err)
	}
	defer memory.Free(bufLimited)
	if len(limited) != 3 {
		t.Fatalf("expected exactly 3 UTXOs with limit=3, got %d", len(limited))
	}
}
