package server

import (
	"bytes"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/keys"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet/serialization"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/txscript"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/utxo"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
	"github.com/HoosatNetwork/HTND/v2/util/txmass"
)

// TestMLDSA44AddressFormsInTheDaemon pins, for each single-sig ML-DSA-44 address form, that the
// address the daemon scans and hands out is one the wallet can sign for with the key pool's
// mnemonic, that the unsigned transaction carries what the signer needs (the redeem script for
// P2SH), and that the mass estimated before signing - which sizes sends and compounds - is the mass
// of the signed transaction.
func TestMLDSA44AddressFormsInTheDaemon(t *testing.T) {
	params := &dagconfig.MainnetParams
	mnemonic, err := libhtnwallet.CreateMnemonic()
	if err != nil {
		t.Fatalf("CreateMnemonic: %+v", err)
	}
	extendedPublicKey, err := libhtnwallet.MasterPublicKeyFromMnemonic(params, mnemonic, false)
	if err != nil {
		t.Fatalf("MasterPublicKeyFromMnemonic: %+v", err)
	}
	pool, err := keys.NewMLDSA44KeyPool(mnemonic, 3, false)
	if err != nil {
		t.Fatalf("NewMLDSA44KeyPool: %+v", err)
	}
	s := &server{
		params:           params,
		keysFile:         &keys.File{ExtendedPublicKeys: []string{extendedPublicKey}, MinimumSignatures: 1, MLDSA44: pool},
		shutdown:         make(chan struct{}),
		addressSet:       make(walletAddressSet),
		txMassCalculator: txmass.NewCalculator(params.MassPerTxByte, params.MassPerScriptPubKeyByte, params.MassPerSigOp),
	}

	scanned := s.mldsa44WalletAddressesForScan(&walletAddress{index: 2, keyChain: libhtnwallet.ExternalKeychain})
	if len(scanned) != 2 {
		t.Fatalf("scanned %d ML-DSA-44 addresses at one index, want 2 (P2PKH, P2SH)", len(scanned))
	}
	wantScriptClass := map[libhtnwallet.MLDSA44AddressForm]txscript.ScriptClass{
		libhtnwallet.MLDSA44AddressFormP2PKH: txscript.PubKeyHashMLDSA44Ty,
		libhtnwallet.MLDSA44AddressFormP2SH:  txscript.ScriptHashTy,
	}
	seen := make(map[string]bool)
	for _, walletAddr := range scanned {
		address, err := s.mldsa44Address(walletAddr)
		if err != nil {
			t.Fatalf("mldsa44Address(%s): %+v", walletAddr.mldsa44Form, err)
		}
		if seen[address.String()] {
			t.Fatalf("two ML-DSA-44 forms at one index share the address %s", address)
		}
		seen[address.String()] = true
		scriptPublicKey, err := txscript.PayToAddrScript(address)
		if err != nil {
			t.Fatalf("PayToAddrScript: %+v", err)
		}
		if got := txscript.GetScriptClass(scriptPublicKey.Script); got != wantScriptClass[walletAddr.mldsa44Form] {
			t.Fatalf("the %s address pays a %s script", walletAddr.mldsa44Form, got)
		}

		coin, err := s.libhtnwalletUTXO(&externalapi.DomainOutpoint{Index: 0},
			utxo.NewUTXOEntry(10_000_000, scriptPublicKey, false, 0), walletAddr)
		if err != nil {
			t.Fatalf("libhtnwalletUTXO: %+v", err)
		}
		if (coin.RedeemScript != nil) != (walletAddr.mldsa44Form == libhtnwallet.MLDSA44AddressFormP2SH) {
			t.Fatalf("the %s coin carries redeem script %x", walletAddr.mldsa44Form, coin.RedeemScript)
		}
		if !allInputsAreMLDSA44([]*libhtnwallet.UTXO{coin}) {
			t.Fatalf("a %s coin is not recognised as ML-DSA-44, so its change would go to a secp256k1 address",
				walletAddr.mldsa44Form)
		}

		unsignedTransaction, err := libhtnwallet.CreateUnsignedTransaction(s.keysFile.ExtendedPublicKeys, 1,
			[]*libhtnwallet.Payment{{Address: address, Amount: 9_000_000}}, []*libhtnwallet.UTXO{coin}, nil)
		if err != nil {
			t.Fatalf("CreateUnsignedTransaction: %+v", err)
		}
		partiallySignedTransaction, err := serialization.DeserializePartiallySignedTransaction(unsignedTransaction)
		if err != nil {
			t.Fatalf("DeserializePartiallySignedTransaction: %+v", err)
		}
		estimatedMass, err := s.estimateMassAfterSignatures(partiallySignedTransaction)
		if err != nil {
			t.Fatalf("estimateMassAfterSignatures: %+v", err)
		}

		signedTransaction, err := libhtnwallet.Sign(params, []string{mnemonic}, unsignedTransaction, false)
		if err != nil {
			t.Fatalf("Sign the %s coin: %+v", walletAddr.mldsa44Form, err)
		}
		tx, err := libhtnwallet.ExtractTransaction(signedTransaction, false)
		if err != nil {
			t.Fatalf("ExtractTransaction: %+v", err)
		}
		if mass := s.txMassCalculator.CalculateTransactionMass(tx); mass != estimatedMass {
			t.Fatalf("the %s spend estimated %d mass before signing and has %d", walletAddr.mldsa44Form, estimatedMass, mass)
		}
		if walletAddr.mldsa44Form == libhtnwallet.MLDSA44AddressFormP2SH &&
			!bytes.HasSuffix(tx.Inputs[0].SignatureScript, coin.RedeemScript) {
			t.Fatalf("the P2SH spend does not end with its redeem script")
		}
	}
}
