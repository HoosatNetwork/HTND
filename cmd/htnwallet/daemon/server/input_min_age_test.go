package server

import (
	"strings"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/keys"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/txscript"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/utxo"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
	"github.com/HoosatNetwork/HTND/v2/util/txmass"
)

// TestSpendableOnlyPastInputMinAge pins the wallet's coin age rule, which is one DAA unit stricter than
// the node's mempool: a coin is spendable once virtual DAA score > its DAA score + inputMinAgeDAAScore,
// plus coinbase maturity for a coinbase output. The coinbase case is 29.9.2026's: outputs of 0d44a4cf,
// stamped at DAA 231080611 by 35b4eeaf, were spent at virtual DAA 231080716 and vanished in a reorg.
func TestSpendableOnlyPastInputMinAge(t *testing.T) {
	const (
		coinbaseMaturity = 100
		stamped          = 231080611
		spentAt          = 231080716
		amount           = 1_266_666_667
	)
	s := &server{coinbaseMaturity: coinbaseMaturity}
	coin := func(isCoinbase bool, daaScore uint64) *walletUTXO {
		return &walletUTXO{
			Outpoint:  &externalapi.DomainOutpoint{},
			UTXOEntry: utxo.NewUTXOEntry(amount, &externalapi.ScriptPublicKey{}, isCoinbase, daaScore),
			address:   &walletAddress{},
		}
	}
	external := func(isCoinbase bool, daaScore uint64) *appmessage.UTXOsByAddressesEntry {
		return &appmessage.UTXOsByAddressesEntry{UTXOEntry: &appmessage.RPCUTXOEntry{
			Amount: amount, BlockDAAScore: daaScore, IsCoinbase: isCoinbase,
		}}
	}
	firstRegular := uint64(stamped + inputMinAgeDAAScore + 1)
	firstCoinbase := firstRegular + coinbaseMaturity

	tests := []struct {
		name       string
		isCoinbase bool
		daaScore   uint64
		virtual    uint64
		want       bool
	}{
		{"coinbase at the DAA it was actually spent at", true, stamped, spentAt, false},
		{"coinbase just past consensus maturity", true, stamped, stamped + coinbaseMaturity + 1, false},
		{"coinbase once a non-coinbase coin would be spendable", true, stamped, firstRegular, false},
		{"coinbase one unit before its age is reached", true, stamped, firstCoinbase - 1, false},
		{"coinbase once its age is reached", true, stamped, firstCoinbase, true},
		{"non-coinbase coin at the DAA the coinbase was spent at", false, stamped, spentAt, false},
		{"non-coinbase coin exactly at the node's boundary", false, stamped, firstRegular - 1, false},
		{"non-coinbase coin once its age is reached", false, stamped, firstRegular, true},
		{"coin of an unconfirmed transaction", false, constants.UnacceptedDAAScore, constants.UnacceptedDAAScore, false},
		{"coin without a DAA score", false, 0, firstRegular, false},
	}
	for _, test := range tests {
		if got := s.isUTXOSpendable(coin(test.isCoinbase, test.daaScore), test.virtual); got != test.want {
			t.Errorf("%s: isUTXOSpendable at virtual DAA %d = %t, want %t", test.name, test.virtual, got, test.want)
		}
		if got := isExternalUTXOSpendable(external(test.isCoinbase, test.daaScore), test.virtual, coinbaseMaturity); got != test.want {
			t.Errorf("%s: isExternalUTXOSpendable at virtual DAA %d = %t, want %t", test.name, test.virtual, got, test.want)
		}
	}
}

// TestBalanceSplitsPendingFromAvailable pins that coins the wallet may not spend yet are reported as
// pending rather than available.
func TestBalanceSplitsPendingFromAvailable(t *testing.T) {
	const virtual = 10_000
	s := &server{coinbaseMaturity: 100}
	balances := balancesMapType{"a": {available: 700}, "b": {available: 50}}
	entry := func(address string, amount, daaScore uint64, isCoinbase bool) *appmessage.UTXOsByAddressesEntry {
		return &appmessage.UTXOsByAddressesEntry{Address: address, UTXOEntry: &appmessage.RPCUTXOEntry{
			Amount: amount, BlockDAAScore: daaScore, IsCoinbase: isCoinbase,
			ScriptPublicKey: &appmessage.RPCScriptPublicKey{},
		}}
	}
	err := s.movePendingOutOfAvailable(balances, []*appmessage.UTXOsByAddressesEntry{
		entry("a", 100, 1, false),                             // old: available
		entry("a", 200, virtual-inputMinAgeDAAScore, false),   // young: pending
		entry("a", 400, virtual-inputMinAgeDAAScore-50, true), // coinbase, old enough as non-coinbase only: pending
		entry("b", 50, virtual-1, false),                      // young: pending
	}, virtual)
	if err != nil {
		t.Fatalf("movePendingOutOfAvailable: %s", err)
	}
	if got := *balances["a"]; got.available != 100 || got.pending != 600 {
		t.Errorf("address a: available %d pending %d, want 100 and 600", got.available, got.pending)
	}
	if got := *balances["b"]; got.available != 0 || got.pending != 50 {
		t.Errorf("address b: available %d pending %d, want 0 and 50", got.available, got.pending)
	}
}

// TestSendOverStandardMassIsRefusedNotSplit pins that a transaction too heavy for one standard
// transaction is refused, instead of being split into transactions whose merge would spend
// unconfirmed outputs - which nodes refuse to accept or relay.
func TestSendOverStandardMassIsRefusedNotSplit(t *testing.T) {
	params := &dagconfig.MainnetParams
	const minimumSignatures = 2
	publicKeys := make([]string, 3)
	for i := range publicKeys {
		mnemonic, err := libhtnwallet.CreateMnemonic()
		if err != nil {
			t.Fatalf("CreateMnemonic: %+v", err)
		}
		publicKeys[i], err = libhtnwallet.MasterPublicKeyFromMnemonic(params, mnemonic, true)
		if err != nil {
			t.Fatalf("MasterPublicKeyFromMnemonic: %+v", err)
		}
	}
	const path = "m/1/2/3"
	address, err := libhtnwallet.Address(params, publicKeys, minimumSignatures, path, false)
	if err != nil {
		t.Fatalf("Address: %+v", err)
	}
	scriptPublicKey, err := txscript.PayToAddrScript(address)
	if err != nil {
		t.Fatalf("PayToAddrScript: %+v", err)
	}
	s := &server{
		params:           params,
		keysFile:         &keys.File{MinimumSignatures: minimumSignatures, ExtendedPublicKeys: publicKeys},
		txMassCalculator: txmass.NewCalculator(params.MassPerTxByte, params.MassPerScriptPubKeyByte, params.MassPerSigOp),
	}
	unsignedWithInputs := func(count int) []byte {
		utxos := make([]*libhtnwallet.UTXO, count)
		for i := range utxos {
			utxos[i] = &libhtnwallet.UTXO{
				Outpoint:       &externalapi.DomainOutpoint{Index: uint32(i)},
				UTXOEntry:      utxo.NewUTXOEntry(1_000_000, scriptPublicKey, false, 1),
				DerivationPath: path,
			}
		}
		unsigned, err := libhtnwallet.CreateUnsignedTransaction(publicKeys, minimumSignatures,
			[]*libhtnwallet.Payment{{Address: address, Amount: 10}}, utxos, nil)
		if err != nil {
			t.Fatalf("CreateUnsignedTransaction: %+v", err)
		}
		return unsigned
	}

	small := unsignedWithInputs(1)
	transactions, err := s.requireStandardMass(small)
	if err != nil {
		t.Fatalf("a one-input transaction was refused: %s", err)
	}
	if len(transactions) != 1 || string(transactions[0]) != string(small) {
		t.Fatalf("a one-input transaction came back as %d transactions", len(transactions))
	}

	transactions, err = s.requireStandardMass(unsignedWithInputs(500))
	if err == nil {
		t.Fatalf("a 500-input multisig transaction was accepted as %d transaction(s)", len(transactions))
	}
	if !strings.Contains(err.Error(), "over the standard limit") {
		t.Fatalf("unexpected error: %s", err)
	}
}
