package server

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/keys"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet/serialization"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/txscript"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/utxo"
	"github.com/HoosatNetwork/HTND/v2/util"
)

// sentTransaction is what a send built: how many of its inputs are ML-DSA-44, and its outputs.
type sentTransaction struct {
	mldsa44Inputs, otherInputs int
	outputs                    []string // the address each output pays, in order
}

func parseSent(t *testing.T, w *feeWallet, transactions [][]byte, err error) sentTransaction {
	t.Helper()
	if err != nil {
		t.Fatalf("the send failed: %+v", err)
	}
	if len(transactions) != 1 {
		t.Fatalf("got %d transactions, want 1", len(transactions))
	}
	partiallySigned, err := serialization.DeserializePartiallySignedTransaction(transactions[0])
	if err != nil {
		t.Fatalf("DeserializePartiallySignedTransaction: %+v", err)
	}
	var sent sentTransaction
	for _, input := range partiallySigned.PartiallySignedInputs {
		if libhtnwallet.IsMLDSA44Input(input) {
			sent.mldsa44Inputs++
		} else {
			sent.otherInputs++
		}
	}
	for _, output := range partiallySigned.Tx.Outputs {
		class, address, err := txscript.ExtractScriptPubKeyAddress(output.ScriptPublicKey, w.params)
		if err != nil {
			t.Fatalf("ExtractScriptPubKeyAddress: %+v", err)
		}
		if address == nil {
			sent.outputs = append(sent.outputs, fmt.Sprintf("%v script %x", class, output.ScriptPublicKey.Script))
			continue
		}
		sent.outputs = append(sent.outputs, address.String())
	}
	return sent
}

// TestMixedSendPaysMLDSA44Change pins that the change of a send that spends an ML-DSA-44 coin goes to
// an ML-DSA-44 address also when the send needs a secp256k1 coin as well.
//
// It used to go there only when every coin of the send was ML-DSA-44. A wallet holding both kinds
// picks its coins largest first whatever their kind, so a send that needed one of each paid its
// change - ML-DSA-44 value - to a secp256k1 address, a pay-to-pubkey one that shows the public key,
// without any message.
func TestMixedSendPaysMLDSA44Change(t *testing.T) {
	schnorr := &walletAddress{index: 1, keyChain: libhtnwallet.ExternalKeychain}
	ml := &walletAddress{index: 2, keyChain: libhtnwallet.ExternalKeychain, mldsa44: true}

	for name, coins := range map[string][]coinSpec{
		"the ML-DSA-44 coin is the larger": {{ml, 500_000_000}, {schnorr, 300_000_000}},
		"the secp256k1 coin is the larger": {{ml, 300_000_000}, {schnorr, 500_000_000}},
	} {
		t.Run(name, func(t *testing.T) {
			w := newMLWallet(t, 1, 1)
			w.fund(t, coins...)
			destination := w.address(t, &walletAddress{index: 2, keyChain: libhtnwallet.ExternalKeychain})
			transactions, err := w.send(destination, 600_000_000, false, nil, false, nil)
			sent := parseSent(t, w, transactions, err)
			if sent.mldsa44Inputs != 1 || sent.otherInputs != 1 || len(sent.outputs) != 2 {
				t.Fatalf("got %+v, want one input of each kind, a payment and change", sent)
			}
			wantChange := w.address(t, &walletAddress{index: 1, keyChain: libhtnwallet.InternalKeychain, mldsa44: true})
			if sent.outputs[1] != wantChange.String() {
				t.Fatalf("the change of a send spending an ML-DSA-44 coin goes to %s, want the ML-DSA-44 change "+
					"address %s", sent.outputs[1], wantChange)
			}
			if tracked, ok := w.s.addressSet[wantChange.String()]; !ok || !tracked.mldsa44 {
				t.Fatalf("the ML-DSA-44 change address is not tracked")
			}
			if got := w.s.keysFile.LastUsedInternalIndex(); got != 1 {
				t.Fatalf("the send used internal indexes up to %d, want 1", got)
			}
		})
	}

	t.Run("a send of secp256k1 coins alone keeps its secp256k1 change", func(t *testing.T) {
		w := newMLWallet(t, 1, 1)
		w.fund(t, coinSpec{schnorr, 500_000_000}, coinSpec{ml, 300_000_000})
		destination := w.address(t, &walletAddress{index: 2, keyChain: libhtnwallet.ExternalKeychain})
		transactions, err := w.send(destination, 400_000_000, false, nil, false, nil)
		sent := parseSent(t, w, transactions, err)
		wantChange := w.address(t, &walletAddress{index: 1, keyChain: libhtnwallet.InternalKeychain})
		if sent.mldsa44Inputs != 0 || len(sent.outputs) != 2 || sent.outputs[1] != wantChange.String() {
			t.Fatalf("got %+v, want secp256k1 inputs only and change to %s", sent, wantChange)
		}
	})

	t.Run("a change address the user named is kept", func(t *testing.T) {
		w := newMLWallet(t, 1, 1)
		w.fund(t, coinSpec{schnorr, 500_000_000}, coinSpec{ml, 300_000_000})
		destination := w.address(t, &walletAddress{index: 2, keyChain: libhtnwallet.ExternalKeychain})
		transactions, err := w.send(destination, 600_000_000, false, []*walletAddress{schnorr, ml}, true, nil)
		sent := parseSent(t, w, transactions, err)
		if sent.mldsa44Inputs != 1 || len(sent.outputs) != 2 || sent.outputs[1] != w.address(t, schnorr).String() {
			t.Fatalf("got %+v, want the change at the first --from-address", sent)
		}
	})

	t.Run("without an ML-DSA-44 key for the change the send is refused", func(t *testing.T) {
		w := newMLWallet(t, 1, 1) // keys for indexes 0-2
		if err := w.s.keysFile.SetLastUsedInternalIndex(2); err != nil {
			t.Fatalf("SetLastUsedInternalIndex: %+v", err)
		}
		w.fund(t, coinSpec{ml, 500_000_000}, coinSpec{schnorr, 300_000_000})
		destination := w.address(t, &walletAddress{index: 2, keyChain: libhtnwallet.ExternalKeychain})
		_, err := w.send(destination, 600_000_000, false, nil, false, nil)
		if err == nil || !strings.Contains(err.Error(), generateMLDSA44KeysSubCmdName) {
			t.Fatalf("got %v, want a refusal naming %s", err, generateMLDSA44KeysSubCmdName)
		}
		if got := w.s.keysFile.LastUsedInternalIndex(); got != 2 {
			t.Fatalf("the refused send moved the internal index to %d", got)
		}
	})
}

// TestMixedSendSwitchesToMLDSA44Change covers a secp256k1 2-of-11 coin that covers the
// amount and the flat fee, but needs an ML-DSA-44 coin to pay the fee for its signed mass.
func TestMixedSendSwitchesToMLDSA44Change(t *testing.T) {
	w := newMLWallet(t, 11, 2)
	schnorr := &walletAddress{index: 1, keyChain: libhtnwallet.ExternalKeychain}
	ml := &walletAddress{index: 1, keyChain: libhtnwallet.ExternalKeychain, mldsa44: true}
	w.fund(t, coinSpec{schnorr, 1_000_000}, coinSpec{ml, 500_000})
	var destination util.Address = w.address(t, ml)

	// The first coin pays 989,000 and the flat 10,000, but its signed mass needs
	// more than 11,000, so the second coin is added.
	transactions, err := w.send(destination, 989_000, false, nil, false, nil)
	sent := parseSent(t, w, transactions, err)
	if sent.mldsa44Inputs != 1 || sent.otherInputs != 1 || len(sent.outputs) != 2 {
		t.Fatalf("got %+v, want one input of each kind, a payment and change", sent)
	}
	wantChange := w.address(t, &walletAddress{index: 1, keyChain: libhtnwallet.InternalKeychain, mldsa44: true})
	if sent.outputs[1] != wantChange.String() {
		t.Fatalf("the change goes to %s, want the ML-DSA-44 change address at internal index 1, %s", sent.outputs[1], wantChange)
	}
	if got := w.s.keysFile.LastUsedInternalIndex(); got != 1 {
		t.Fatalf("the send used internal indexes up to %d, want 1", got)
	}
}

func TestExhaustedMLDSA44ChangePoolAllowsNoChange(t *testing.T) {
	for _, tt := range []struct {
		name      string
		remainder uint64
		wantError bool
	}{
		{"exact payment", 0, false},
		{"dust remainder", 608, false},
		{"non-dust remainder", 609, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w := newMLWallet(t, 1, 1)
			if err := w.s.keysFile.SetLastUsedInternalIndex(2); err != nil {
				t.Fatal(err)
			}
			ml := &walletAddress{index: 2, keyChain: libhtnwallet.ExternalKeychain, mldsa44: true}
			schnorr := &walletAddress{index: 1, keyChain: libhtnwallet.ExternalKeychain}
			w.fund(t, coinSpec{ml, 500_000_000}, coinSpec{schnorr, 300_000_000})
			destination := w.address(t, schnorr)
			amount := 799_980_000 - tt.remainder
			attachSendTestNode(t, w)
			trackedBefore := len(w.s.addressSet)
			txs, err := w.send(destination, amount, false, nil, false, nil)
			if tt.wantError {
				if err == nil || !strings.Contains(err.Error(), generateMLDSA44KeysSubCmdName) || len(txs) != 0 {
					t.Fatalf("got %d transactions and %v, want the missing-change-key error", len(txs), err)
				}
			} else {
				result := w.check(txs, err)
				wantOutputs(t, result, amount)
				if result.fee != 20_000+tt.remainder {
					t.Fatalf("paid fee %d, want %d", result.fee, 20_000+tt.remainder)
				}
			}
			if got := w.s.keysFile.LastUsedInternalIndex(); got != 2 || len(w.s.addressSet) != trackedBefore {
				t.Fatalf("index=%d, tracked %d addresses, want index 2 and no new addresses", got, len(w.s.addressSet))
			}
		})
	}
}

func TestExhaustedMLDSA44ChangePoolCanUseClassicalCoins(t *testing.T) {
	w := newMLWallet(t, 1, 1)
	if err := w.s.keysFile.SetLastUsedInternalIndex(2); err != nil {
		t.Fatal(err)
	}
	ml := &walletAddress{index: 2, keyChain: libhtnwallet.ExternalKeychain, mldsa44: true}
	schnorr := &walletAddress{index: 1, keyChain: libhtnwallet.ExternalKeychain}
	w.fund(t, coinSpec{ml, 500_000_000}, coinSpec{schnorr, 310_000_000}, coinSpec{schnorr, 300_000_000})
	txs, err := w.send(w.address(t, schnorr), 600_000_000, false, nil, false, nil)
	sent := parseSent(t, w, txs, err)
	if sent.mldsa44Inputs != 0 || sent.otherInputs != 2 || len(sent.outputs) != 2 {
		t.Fatalf("got %+v, want two classical inputs and change", sent)
	}
	wantOutputs(t, w.check(txs, err), 600_000_000, 9_980_000)
	if got := w.s.keysFile.LastUsedInternalIndex(); got != 3 {
		t.Fatalf("index=%d, want 3", got)
	}
}

func TestRefusedMixedSendDoesNotReserveChange(t *testing.T) {
	w := newMLWallet(t, 11, 2)
	if err := w.s.keysFile.SetLastUsedInternalIndex(2); err != nil {
		t.Fatal(err)
	}
	schnorr := &walletAddress{index: 1, keyChain: libhtnwallet.ExternalKeychain}
	ml := &walletAddress{index: 1, keyChain: libhtnwallet.ExternalKeychain, mldsa44: true}
	w.fund(t, coinSpec{schnorr, 1_000_000}, coinSpec{ml, 500_000})
	attachSendTestNode(t, w)
	trackedBefore := len(w.s.addressSet)
	for i := 0; i < 3; i++ {
		txs, err := w.send(w.address(t, ml), 989_000, false, nil, false, nil)
		if err == nil || !strings.Contains(err.Error(), generateMLDSA44KeysSubCmdName) || len(txs) != 0 {
			t.Fatalf("attempt %d returned %d transactions and %v, want the missing-change-key error", i, len(txs), err)
		}
	}
	saved, err := keys.ReadKeysFile(w.params, w.s.keysFile.Path())
	if err != nil {
		t.Fatal(err)
	}
	if got := saved.LastUsedInternalIndex(); got != 2 {
		t.Errorf("persisted index=%d, want 2", got)
	}
	if got := w.s.keysFile.LastUsedInternalIndex(); got != 2 || len(w.s.addressSet) != trackedBefore {
		t.Errorf("in-memory index=%d, tracked %d addresses, want index 2 and no new addresses", got, len(w.s.addressSet))
	}
}

func TestNamedChangePreservesSchnorrAddressForm(t *testing.T) {
	for _, tt := range []struct {
		name string
		form libhtnwallet.SingleSigAddressType
	}{
		{"P2PKH", libhtnwallet.SingleSigAddressTypeP2PKH},
		{"P2SH", libhtnwallet.SingleSigAddressTypeP2SH},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w := newMLWallet(t, 1, 1)
			schnorr := &walletAddress{index: 1, keyChain: libhtnwallet.ExternalKeychain}
			ml := &walletAddress{index: 2, keyChain: libhtnwallet.ExternalKeychain, mldsa44: true}
			w.fund(t, coinSpec{schnorr, 300_000_000}, coinSpec{ml, 500_000_000})
			named, err := libhtnwallet.AddressWithSingleSigAddressType(w.params,
				w.s.keysFile.ExtendedPublicKeys, 1, w.s.walletAddressPath(schnorr), false, tt.form)
			if err != nil {
				t.Fatal(err)
			}
			script, err := txscript.PayToAddrScript(named)
			if err != nil {
				t.Fatal(err)
			}
			w.s.utxosSortedByAmount[0].UTXOEntry = utxo.NewUTXOEntry(300_000_000, script, false, 1)
			mlAddress := w.address(t, ml)
			w.s.addressSet[named.String()] = schnorr
			w.s.addressSet[mlAddress.String()] = ml
			txs, err := w.s.buildSendTransaction(w.address(t, schnorr), 600_000_000, false,
				[]string{named.String(), mlAddress.String()}, true, nil, feeTestVirtualDAAScore)
			if err != nil {
				t.Fatal(err)
			}
			sent := parseSent(t, w, [][]byte{txs.transaction}, nil)
			if sent.mldsa44Inputs != 1 || sent.otherInputs != 1 || len(sent.outputs) != 2 || sent.outputs[1] != named.String() {
				t.Fatalf("got %+v, want one input of each type with change to %s", sent, named)
			}
			if got := w.s.keysFile.LastUsedInternalIndex(); got != 0 {
				t.Fatalf("index=%d, want 0", got)
			}
		})
	}
}

func TestMixedSendAtMassLimitCanUseClassicalCoins(t *testing.T) {
	w := newMLWallet(t, 1, 1)
	ml := &walletAddress{index: 1, keyChain: libhtnwallet.ExternalKeychain, mldsa44: true}
	mlP2SH := &walletAddress{index: 2, keyChain: libhtnwallet.ExternalKeychain, mldsa44: true,
		mldsa44Form: libhtnwallet.MLDSA44AddressFormP2SH}
	schnorr := &walletAddress{index: 1, keyChain: libhtnwallet.ExternalKeychain}
	coins := []coinSpec{{mlP2SH, 1_000_000_000}}
	coins = append(coins, same(ml, repeat(1_000_000_000, 15)...)...)
	coins = append(coins, same(schnorr, repeat(900_000_000, 38)...)...)
	w.fund(t, coins...)
	const amount = 335 * 100_000_000
	destination := w.address(t, schnorr)

	txs, err := w.send(destination, amount, false, nil, false, nil)
	sent := parseSent(t, w, txs, err)
	if sent.mldsa44Inputs != 0 || sent.otherInputs != 38 || len(sent.outputs) != 2 {
		t.Fatalf("got %+v, want the payment with only classical inputs and change", sent)
	}
	wantOutputs(t, w.check(txs, err), amount, 699_620_000)

	txs, err = w.send(destination, amount, false, []*walletAddress{ml, mlP2SH, schnorr}, false, nil)
	if err == nil || !strings.Contains(err.Error(), "over the standard limit") || len(txs) != 0 {
		t.Fatalf("the explicitly restricted send returned %d transactions and %v", len(txs), err)
	}
}

func TestExhaustedFallbackSurfacesSaveError(t *testing.T) {
	w := newMLWallet(t, 1, 1)
	if err := w.s.keysFile.SetLastUsedInternalIndex(2); err != nil {
		t.Fatal(err)
	}
	path := w.s.keysFile.Path()
	backup := path + ".backup"
	if err := os.Rename(path, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	ml := &walletAddress{index: 2, keyChain: libhtnwallet.ExternalKeychain, mldsa44: true}
	schnorr := &walletAddress{index: 1, keyChain: libhtnwallet.ExternalKeychain}
	w.fund(t, coinSpec{ml, 500_000_000}, coinSpec{schnorr, 310_000_000}, coinSpec{schnorr, 300_000_000})
	attachSendTestNode(t, w)
	trackedBefore := len(w.s.addressSet)
	txs, err := w.send(w.address(t, schnorr), 600_000_000, false, nil, false, nil)
	var renameErr *os.LinkError
	if len(txs) != 0 || !errors.As(err, &renameErr) || renameErr.Op != "rename" {
		t.Errorf("got %d transactions and %v, want a rename error", len(txs), err)
	}
	saved, err := keys.ReadKeysFile(w.params, backup)
	if err != nil {
		t.Fatal(err)
	}
	if got := w.s.keysFile.LastUsedInternalIndex(); got != saved.LastUsedInternalIndex() || len(w.s.addressSet) != trackedBefore {
		t.Fatalf("index=%d, saved index=%d, tracked %d addresses", got, saved.LastUsedInternalIndex(), len(w.s.addressSet))
	}
}

func TestExhaustedPoolExactMixedSubset(t *testing.T) {
	w := newMLWallet(t, 1, 1)
	if err := w.s.keysFile.SetLastUsedInternalIndex(2); err != nil {
		t.Fatal(err)
	}
	ml := &walletAddress{index: 1, keyChain: libhtnwallet.ExternalKeychain, mldsa44: true}
	four := &walletAddress{index: 1, keyChain: libhtnwallet.ExternalKeychain}
	three := &walletAddress{index: 2, keyChain: libhtnwallet.ExternalKeychain}
	w.fund(t, coinSpec{ml, 500_000_000}, coinSpec{four, 400_000_000}, coinSpec{three, 300_000_000})
	destination := w.address(t, four)
	const amount = 799_980_000
	txs, err := w.send(destination, amount, false, nil, false, nil)
	result := w.check(txs, err)
	wantOutputs(t, result, amount)
	if result.inputs != 2 || result.fee != 20_000 || w.s.keysFile.LastUsedInternalIndex() != 2 {
		t.Fatalf("got %+v at change index %d, want two inputs, a 20,000-sompi fee and index 2",
			result, w.s.keysFile.LastUsedInternalIndex())
	}
}
