package server

import (
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/cmd/htnwallet/keys"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/utxo"
)

// TestSelectMoreUTXOsForMergeTransaction pins which coins a merge transaction may add as extra funds: never
// one the split transactions already spend, never one recently spent by another transaction, and never one
// worth no more than its fee (whose net value used to wrap around and satisfy any requirement).
func TestSelectMoreUTXOsForMergeTransaction(t *testing.T) {
	const virtualDAAScore = 100
	newCoin := func(id byte, amount uint64) *walletUTXO {
		return &walletUTXO{
			Outpoint:  &externalapi.DomainOutpoint{TransactionID: *externalapi.NewDomainTransactionIDFromByteArray(&[externalapi.DomainHashSize]byte{id})},
			UTXOEntry: utxo.NewUTXOEntry(amount, &externalapi.ScriptPublicKey{}, false, 1),
			address:   &walletAddress{},
		}
	}
	spentBySplit := newCoin(1, 5_000_000)
	usedRecently := newCoin(2, 4_000_000)
	dust := newCoin(3, feePerInput/2)
	free := newCoin(4, 3_000_000)

	s := &server{
		keysFile:            &keys.File{ExtendedPublicKeys: []string{"xpub"}, MinimumSignatures: 1},
		utxosSortedByAmount: []*walletUTXO{dust, free, usedRecently, spentBySplit},
		usedOutpoints:       map[externalapi.DomainOutpoint]time.Time{*usedRecently.Outpoint: time.Now()},
	}
	excluded := map[externalapi.DomainOutpoint]struct{}{*spentBySplit.Outpoint: {}}

	selected, totalAdded, err := s.selectMoreUTXOsForMergeTransaction(excluded, 1_000_000, virtualDAAScore)
	if err != nil {
		t.Fatalf("selectMoreUTXOsForMergeTransaction: %+v", err)
	}
	if len(selected) != 1 || *selected[0].Outpoint != *free.Outpoint {
		t.Fatalf("expected only the free coin to be selected, got %d coins", len(selected))
	}
	if want := free.UTXOEntry.Amount() - feePerInput; totalAdded != want {
		t.Fatalf("total added is %d, want %d", totalAdded, want)
	}

	// Asking for more than the free coin holds must fail rather than reach for the excluded or used coins.
	_, _, err = s.selectMoreUTXOsForMergeTransaction(excluded, 3_000_000, virtualDAAScore)
	if err == nil {
		t.Fatalf("expected insufficient funds when only excluded, recently used and dust coins remain")
	}
}
