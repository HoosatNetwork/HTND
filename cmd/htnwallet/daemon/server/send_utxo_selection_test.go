package server

import (
	"math"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/keys"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/utxo"
)

// TestSelectUTXOsForTransactionFeeArithmetic pins that coin selection for a send never returns an amount
// that wrapped around: a send-all over coins worth less than their fees, and a requested amount near the
// uint64 limit, both used to yield a payment of about 2^64 sompi that passed the funds check.
func TestSelectUTXOsForTransactionFeeArithmetic(t *testing.T) {
	const virtualDAAScore = 100
	newServer := func(amounts ...uint64) *server {
		coins := make([]*walletUTXO, len(amounts))
		for i, amount := range amounts {
			coins[i] = &walletUTXO{
				Outpoint: &externalapi.DomainOutpoint{
					TransactionID: *externalapi.NewDomainTransactionIDFromByteArray(&[externalapi.DomainHashSize]byte{byte(i + 1)}),
				},
				UTXOEntry: utxo.NewUTXOEntry(amount, &externalapi.ScriptPublicKey{}, false, 1),
				address:   &walletAddress{},
			}
		}
		return &server{
			keysFile:            &keys.File{ExtendedPublicKeys: []string{"xpub"}, MinimumSignatures: 1},
			utxosSortedByAmount: coins,
		}
	}

	t.Run("send all with coins worth less than their fees", func(t *testing.T) {
		s := newServer(feePerInput/2, feePerInput/4)
		_, received, _, err := s.selectUTXOsForTransactionAtDAAScore(0, true, feePerInput, nil, virtualDAAScore)
		if err == nil {
			t.Fatalf("expected insufficient funds, got a payment of %d sompi", received)
		}
	})

	t.Run("send all", func(t *testing.T) {
		s := newServer(5_000_000, 3_000_000)
		selected, received, change, err := s.selectUTXOsForTransactionAtDAAScore(0, true, feePerInput, nil, virtualDAAScore)
		if err != nil {
			t.Fatalf("selectUTXOsForTransactionAtDAAScore: %+v", err)
		}
		if len(selected) != 2 || received != 8_000_000-2*feePerInput || change != 0 {
			t.Fatalf("got %d coins, received %d, change %d", len(selected), received, change)
		}
	})

	t.Run("amount near the uint64 limit", func(t *testing.T) {
		s := newServer(5_000_000, 3_000_000)
		_, received, _, err := s.selectUTXOsForTransactionAtDAAScore(math.MaxUint64-feePerInput+1, false, feePerInput, nil, virtualDAAScore)
		if err == nil {
			t.Fatalf("expected an error, got a payment of %d sompi", received)
		}
	})

	t.Run("send", func(t *testing.T) {
		s := newServer(5_000_000, 3_000_000)
		selected, received, change, err := s.selectUTXOsForTransactionAtDAAScore(4_000_000, false, feePerInput, nil, virtualDAAScore)
		if err != nil {
			t.Fatalf("selectUTXOsForTransactionAtDAAScore: %+v", err)
		}
		if len(selected) != 1 || received != 4_000_000 || change != 1_000_000-feePerInput {
			t.Fatalf("got %d coins, received %d, change %d", len(selected), received, change)
		}
	})
}
