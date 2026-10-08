package txscript

import (
	"testing"

	"github.com/cloudflare/circl/sign/mldsa/mldsa44"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/utxo"
	"github.com/HoosatNetwork/HTND/v2/util"
)

// TestMLDSA44PayToScriptHashSingleSig pins single-sig ML-DSA-44 behind P2SH: the redeem script is
// the P2PKH script, as the wallet's Schnorr and ECDSA single-sig P2SH addresses wrap theirs, so the
// signature script is <signature> <public key> <redeem script>.
func TestMLDSA44PayToScriptHashSingleSig(t *testing.T) {
	publicKey, privateKey := mldsa44TestKey(t, 1)
	otherPublicKey, otherPrivateKey := mldsa44TestKey(t, 2)

	p2pkhAddress, err := util.NewAddressPublicKeyHashMLDSA44(publicKey.Bytes(), util.Bech32PrefixHoosat)
	if err != nil {
		t.Fatalf("NewAddressPublicKeyHashMLDSA44: %v", err)
	}
	redeemScript, err := PayToAddrScript(p2pkhAddress)
	if err != nil {
		t.Fatalf("PayToAddrScript: %v", err)
	}
	p2sh, err := PayToScriptHashScript(redeemScript.Script)
	if err != nil {
		t.Fatalf("PayToScriptHashScript: %v", err)
	}
	scriptPubKey := &externalapi.ScriptPublicKey{Script: p2sh}

	spend := func(privateKey *mldsa44.PrivateKey, publicKey *mldsa44.PublicKey, flags ScriptFlags) (*externalapi.DomainTransaction, error) {
		tx := newMinimalTestTxWith(nil, 0, 0)
		tx.Inputs[0].UTXOEntry = utxo.NewUTXOEntry(500, scriptPubKey, false, 100)
		tx.Outputs[0].ScriptPublicKey = scriptPubKey
		signature, err := RawTxInSignatureMLDSA44(tx, 0, consensushashing.SigHashAll, privateKey, &consensushashing.SighashReusedValues{})
		if err != nil {
			t.Fatalf("RawTxInSignatureMLDSA44: %v", err)
		}
		sigScript, err := NewScriptBuilder().
			AddFullData(signature).
			AddFullData(publicKey.Bytes()).
			AddData(redeemScript.Script).
			Script()
		if err != nil {
			t.Fatalf("build signature script: %v", err)
		}
		tx.Inputs[0].SignatureScript = sigScript
		return tx, executeMLDSA44(tx, scriptPubKey, flags)
	}

	tx, err := spend(privateKey, publicKey, ScriptEnableMLDSA44)
	if err != nil {
		t.Fatalf("a correctly signed single-sig P2SH spend failed: %v", err)
	}
	if got := GetPreciseSigOpCountWithFlags(tx.Inputs[0].SignatureScript, scriptPubKey, ScriptEnableMLDSA44); got != 1 {
		t.Fatalf("single-sig P2SH sigops: got %d, want 1", got)
	}
	if _, err := spend(otherPrivateKey, otherPublicKey, ScriptEnableMLDSA44); !IsErrorCode(err, ErrEqualVerify) {
		t.Fatalf("single-sig P2SH spend by another key: want ErrEqualVerify, got %v", err)
	}
	if _, err := spend(privateKey, publicKey, ScriptNoFlags); !IsErrorCode(err, ErrElementTooBig) {
		t.Fatalf("pre-activation single-sig P2SH spend: want ErrElementTooBig, got %v", err)
	}
}
