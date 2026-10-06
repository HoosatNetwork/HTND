package txscript

import (
	"testing"

	"github.com/cloudflare/circl/sign/mldsa/mldsa44"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/utxo"
	"github.com/HoosatNetwork/HTND/v2/util"
)

type mldsa44TestSigner struct {
	publicKey  *mldsa44.PublicKey
	privateKey *mldsa44.PrivateKey
}

func mldsa44TestSigners(t *testing.T, count int) ([]mldsa44TestSigner, [][]byte) {
	t.Helper()
	signers := make([]mldsa44TestSigner, count)
	hashes := make([][]byte, count)
	for i := range signers {
		publicKey, privateKey := mldsa44TestKey(t, byte(10+i))
		signers[i] = mldsa44TestSigner{publicKey: publicKey, privateKey: privateKey}
		hashes[i] = util.HashBlake2b(publicKey.Bytes())
	}
	return signers, hashes
}

// newMLDSA44MultiSigSpendTx returns a transaction spending a P2SH output locked to redeemScript.
func newMLDSA44MultiSigSpendTx(t *testing.T, redeemScript []byte) (*externalapi.DomainTransaction, *externalapi.ScriptPublicKey) {
	t.Helper()
	p2sh, err := PayToScriptHashScript(redeemScript)
	if err != nil {
		t.Fatalf("PayToScriptHashScript: %v", err)
	}
	scriptPubKey := &externalapi.ScriptPublicKey{Script: p2sh}
	tx := newMinimalTestTxWith(nil, 0, 0)
	tx.Inputs[0].UTXOEntry = utxo.NewUTXOEntry(500, scriptPubKey, false, 100)
	tx.Outputs[0].ScriptPublicKey = scriptPubKey
	return tx, scriptPubKey
}

// signMLDSA44MultiSig signs tx with the signers at signingIndexes and returns its signature script.
func signMLDSA44MultiSig(t *testing.T, tx *externalapi.DomainTransaction, redeemScript []byte,
	signers []mldsa44TestSigner, signingIndexes ...int,
) []byte {
	t.Helper()
	signatures := make([][]byte, len(signers))
	publicKeys := make([][]byte, len(signers))
	for _, i := range signingIndexes {
		signature, err := RawTxInSignatureMLDSA44(tx, 0, consensushashing.SigHashAll, signers[i].privateKey,
			&consensushashing.SighashReusedValues{})
		if err != nil {
			t.Fatalf("RawTxInSignatureMLDSA44: %v", err)
		}
		signatures[i] = signature
		publicKeys[i] = signers[i].publicKey.Bytes()
	}
	sigScript, err := MultiSigMLDSA44SignatureScript(redeemScript, signatures, publicKeys)
	if err != nil {
		t.Fatalf("MultiSigMLDSA44SignatureScript: %v", err)
	}
	return sigScript
}

func TestMLDSA44MultiSigSpends(t *testing.T) {
	tests := []struct {
		name           string
		keyCount       int
		required       int
		signingIndexes []int
	}{
		{name: "1-of-2 by the first key", keyCount: 2, required: 1, signingIndexes: []int{0}},
		{name: "1-of-2 by the second key", keyCount: 2, required: 1, signingIndexes: []int{1}},
		{name: "2-of-3 by keys 1 and 3", keyCount: 3, required: 2, signingIndexes: []int{0, 2}},
		{name: "2-of-3 by keys 2 and 3", keyCount: 3, required: 2, signingIndexes: []int{1, 2}},
		{name: "2-of-11 by the last two keys", keyCount: 11, required: 2, signingIndexes: []int{9, 10}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			signers, hashes := mldsa44TestSigners(t, test.keyCount)
			redeemScript, err := MultiSigMLDSA44RedeemScript(hashes, test.required)
			if err != nil {
				t.Fatalf("MultiSigMLDSA44RedeemScript: %v", err)
			}
			tx, scriptPubKey := newMLDSA44MultiSigSpendTx(t, redeemScript)
			tx.Inputs[0].SignatureScript = signMLDSA44MultiSig(t, tx, redeemScript, signers, test.signingIndexes...)

			if err := executeMLDSA44(tx, scriptPubKey, ScriptEnableMLDSA44); err != nil {
				t.Fatalf("a valid ML-DSA-44 multisig spend failed: %v", err)
			}
			if err := executeMLDSA44(tx, scriptPubKey, ScriptNoFlags); err == nil {
				t.Fatalf("an ML-DSA-44 multisig spend succeeded before activation")
			}
			if got := GetPreciseSigOpCountWithFlags(tx.Inputs[0].SignatureScript, scriptPubKey, ScriptEnableMLDSA44); got != test.keyCount {
				t.Fatalf("sigops: got %d, want %d (one per key)", got, test.keyCount)
			}
		})
	}
}

func TestMLDSA44MultiSigRejectsBadSpends(t *testing.T) {
	signers, hashes := mldsa44TestSigners(t, 3)
	redeemScript, err := MultiSigMLDSA44RedeemScript(hashes, 2)
	if err != nil {
		t.Fatalf("MultiSigMLDSA44RedeemScript: %v", err)
	}

	t.Run("one signature of the two required", func(t *testing.T) {
		tx, scriptPubKey := newMLDSA44MultiSigSpendTx(t, redeemScript)
		// Build the script by hand, since MultiSigMLDSA44SignatureScript refuses to.
		signature, err := RawTxInSignatureMLDSA44(tx, 0, consensushashing.SigHashAll, signers[0].privateKey,
			&consensushashing.SighashReusedValues{})
		if err != nil {
			t.Fatalf("RawTxInSignatureMLDSA44: %v", err)
		}
		sigScript, err := NewScriptBuilder().AddOp(Op0).AddOp(Op0).
			AddFullData(signature).AddFullData(signers[0].publicKey.Bytes()).AddOp(Op1).
			AddData(redeemScript).Script()
		if err != nil {
			t.Fatalf("build signature script: %v", err)
		}
		tx.Inputs[0].SignatureScript = sigScript
		if err := executeMLDSA44(tx, scriptPubKey, ScriptEnableMLDSA44); !IsErrorCode(err, ErrEvalFalse) {
			t.Fatalf("want ErrEvalFalse, got %v", err)
		}
	})

	t.Run("a key signing in another key's slot", func(t *testing.T) {
		tx, scriptPubKey := newMLDSA44MultiSigSpendTx(t, redeemScript)
		signatures := make([][]byte, 3)
		publicKeys := make([][]byte, 3)
		for _, slot := range []int{0, 1} {
			signature, err := RawTxInSignatureMLDSA44(tx, 0, consensushashing.SigHashAll, signers[2].privateKey,
				&consensushashing.SighashReusedValues{})
			if err != nil {
				t.Fatalf("RawTxInSignatureMLDSA44: %v", err)
			}
			signatures[slot] = signature
			publicKeys[slot] = signers[2].publicKey.Bytes()
		}
		sigScript, err := MultiSigMLDSA44SignatureScript(redeemScript, signatures, publicKeys)
		if err != nil {
			t.Fatalf("MultiSigMLDSA44SignatureScript: %v", err)
		}
		tx.Inputs[0].SignatureScript = sigScript
		if err := executeMLDSA44(tx, scriptPubKey, ScriptEnableMLDSA44); !IsErrorCode(err, ErrEqualVerify) {
			t.Fatalf("want ErrEqualVerify, got %v", err)
		}
	})

	t.Run("a forged signature", func(t *testing.T) {
		tx, scriptPubKey := newMLDSA44MultiSigSpendTx(t, redeemScript)
		sigScript := signMLDSA44MultiSig(t, tx, redeemScript, signers, 0, 1)
		tx.Inputs[0].SignatureScript = sigScript
		tx.Outputs[0].Value++
		if err := executeMLDSA44(tx, scriptPubKey, ScriptEnableMLDSA44); !IsErrorCode(err, ErrNullFail) {
			t.Fatalf("want ErrNullFail, got %v", err)
		}
	})
}

// TestMLDSA44MultiSigLimitsFitConsensusSizes pins that the largest multisig these helpers build fits
// the consensus limits the design is sized by, and that one key or signature more is refused.
func TestMLDSA44MultiSigLimitsFitConsensusSizes(t *testing.T) {
	signers, hashes := mldsa44TestSigners(t, MaxMLDSA44MultiSigKeys)
	redeemScript, err := MultiSigMLDSA44RedeemScript(hashes, MaxMLDSA44MultiSigSignatures)
	if err != nil {
		t.Fatalf("MultiSigMLDSA44RedeemScript: %v", err)
	}
	if len(redeemScript) > MaxScriptElementSize {
		t.Fatalf("an %d-key redeem script is %d bytes, over MaxScriptElementSize", MaxMLDSA44MultiSigKeys, len(redeemScript))
	}
	tx, _ := newMLDSA44MultiSigSpendTx(t, redeemScript)
	sigScript := signMLDSA44MultiSig(t, tx, redeemScript, signers, 0, 1)
	if len(sigScript) > MaxScriptSize {
		t.Fatalf("a %d-of-%d signature script is %d bytes, over MaxScriptSize", MaxMLDSA44MultiSigSignatures,
			MaxMLDSA44MultiSigKeys, len(sigScript))
	}
	t.Logf("largest ML-DSA-44 multisig signature script: %d bytes", len(sigScript))

	_, tooMany := mldsa44TestSigners(t, MaxMLDSA44MultiSigKeys+1)
	if _, err := MultiSigMLDSA44RedeemScript(tooMany, 2); err == nil {
		t.Fatalf("built a redeem script with %d keys", MaxMLDSA44MultiSigKeys+1)
	}
	if _, err := MultiSigMLDSA44RedeemScript(hashes[:3], MaxMLDSA44MultiSigSignatures+1); err == nil {
		t.Fatalf("built a redeem script requiring %d signatures", MaxMLDSA44MultiSigSignatures+1)
	}

	required, extracted, err := ExtractMultiSigMLDSA44RedeemScript(redeemScript)
	if err != nil || required != MaxMLDSA44MultiSigSignatures || len(extracted) != MaxMLDSA44MultiSigKeys {
		t.Fatalf("ExtractMultiSigMLDSA44RedeemScript: required=%d keys=%d err=%v", required, len(extracted), err)
	}
	if IsMultiSigMLDSA44RedeemScript(append(append([]byte{}, redeemScript...), OpNop)) {
		t.Fatalf("a redeem script with a trailing opcode was recognised")
	}
}
