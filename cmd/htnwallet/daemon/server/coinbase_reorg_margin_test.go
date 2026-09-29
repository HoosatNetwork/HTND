package server

import (
	"testing"

	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/utxo"
)

// TestCoinbaseSpendableOnlyPastReorgSafetyMargin pins the case of 29.9.2026: coinbase outputs of 0d44a4cf,
// stamped at DAA 231080611 by 35b4eeaf, were spent at virtual DAA 231080716 - past the 100-unit consensus
// maturity - and vanished when virtual reorged away from 35b4eeaf. The wallet must not select such a coin
// until coinbaseReorgSafetyMargin more units have passed. Non-coinbase coins are unaffected.
func TestCoinbaseSpendableOnlyPastReorgSafetyMargin(t *testing.T) {
	const (
		coinbaseMaturity = 100
		stamped          = 231080611
		spentAt          = 231080716
		amount           = 1_266_666_667
	)
	s := &server{coinbaseMaturity: coinbaseMaturity}
	coin := func(isCoinbase bool) *walletUTXO {
		return &walletUTXO{
			Outpoint:  &externalapi.DomainOutpoint{},
			UTXOEntry: utxo.NewUTXOEntry(amount, &externalapi.ScriptPublicKey{}, isCoinbase, stamped),
			address:   &walletAddress{},
		}
	}
	external := func(isCoinbase bool) *appmessage.UTXOsByAddressesEntry {
		return &appmessage.UTXOsByAddressesEntry{UTXOEntry: &appmessage.RPCUTXOEntry{
			Amount: amount, BlockDAAScore: stamped, IsCoinbase: isCoinbase,
		}}
	}
	firstSafe := uint64(stamped + coinbaseMaturity + coinbaseReorgSafetyMargin + 1)

	tests := []struct {
		name       string
		isCoinbase bool
		virtual    uint64
		want       bool
	}{
		{"coinbase at the DAA it was actually spent at", true, spentAt, false},
		{"coinbase just past consensus maturity", true, stamped + coinbaseMaturity + 1, false},
		{"coinbase one unit before the safety margin ends", true, firstSafe - 1, false},
		{"coinbase once the safety margin has passed", true, firstSafe, true},
		{"non-coinbase coin at the same DAA", false, spentAt, true},
	}
	for _, test := range tests {
		if got := s.isUTXOSpendable(coin(test.isCoinbase), test.virtual); got != test.want {
			t.Errorf("%s: isUTXOSpendable at virtual DAA %d = %t, want %t", test.name, test.virtual, got, test.want)
		}
		if got := isExternalUTXOSpendable(external(test.isCoinbase), test.virtual, coinbaseMaturity); got != test.want {
			t.Errorf("%s: isExternalUTXOSpendable at virtual DAA %d = %t, want %t", test.name, test.virtual, got, test.want)
		}
	}
}
