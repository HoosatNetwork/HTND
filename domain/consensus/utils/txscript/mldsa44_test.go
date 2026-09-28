package txscript

import (
	"bytes"
	stdmldsa "crypto/mldsa"
	"testing"

	"github.com/cloudflare/circl/sign/mldsa/mldsa44"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/utxo"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
	"github.com/HoosatNetwork/HTND/v2/util"
)

// mldsa44TestKey derives a fixed key so failures reproduce.
func mldsa44TestKey(t *testing.T, seedByte byte) (*mldsa44.PublicKey, *mldsa44.PrivateKey) {
	t.Helper()
	var seed [mldsa44.SeedSize]byte
	for i := range seed {
		seed[i] = seedByte
	}
	return mldsa44.NewKeyFromSeed(&seed)
}

// newMLDSA44SpendTx returns a one-input transaction spending an ML-DSA-44 P2PKH output locked
// to publicKey, together with that output's script.
func newMLDSA44SpendTx(t *testing.T, publicKey *mldsa44.PublicKey) (*externalapi.DomainTransaction, *externalapi.ScriptPublicKey) {
	t.Helper()
	address, err := util.NewAddressPublicKeyHashMLDSA44(publicKey.Bytes(), util.Bech32PrefixHoosat)
	if err != nil {
		t.Fatalf("NewAddressPublicKeyHashMLDSA44: %v", err)
	}
	scriptPubKey, err := PayToAddrScript(address)
	if err != nil {
		t.Fatalf("PayToAddrScript: %v", err)
	}
	tx := newMinimalTestTxWith(nil, 0, 0)
	tx.Inputs[0].UTXOEntry = utxo.NewUTXOEntry(500, scriptPubKey, false, 100)
	tx.Outputs[0].ScriptPublicKey = scriptPubKey
	return tx, scriptPubKey
}

func executeMLDSA44(tx *externalapi.DomainTransaction, scriptPubKey *externalapi.ScriptPublicKey, flags ScriptFlags) error {
	vm, err := NewEngine(scriptPubKey, tx, 0, flags, nil, nil, &consensushashing.SighashReusedValues{})
	if err != nil {
		return err
	}
	return vm.Execute()
}

func TestMLDSA44SizesMatchAddressPackage(t *testing.T) {
	if util.PublicKeySizeMLDSA44 != mldsa44.PublicKeySize {
		t.Fatalf("util.PublicKeySizeMLDSA44 is %d, CIRCL's ML-DSA-44 public key is %d bytes",
			util.PublicKeySizeMLDSA44, mldsa44.PublicKeySize)
	}
}

func TestMLDSA44SpendActivatedByFlagOnly(t *testing.T) {
	publicKey, privateKey := mldsa44TestKey(t, 1)
	tx, scriptPubKey := newMLDSA44SpendTx(t, publicKey)

	sigScript, err := SignatureScriptMLDSA44(tx, 0, consensushashing.SigHashAll, privateKey, &consensushashing.SighashReusedValues{})
	if err != nil {
		t.Fatalf("SignatureScriptMLDSA44: %v", err)
	}
	tx.Inputs[0].SignatureScript = sigScript

	if err := executeMLDSA44(tx, scriptPubKey, ScriptEnableMLDSA44); err != nil {
		t.Fatalf("a correctly signed ML-DSA-44 spend failed under ScriptEnableMLDSA44: %v", err)
	}

	// Before activation the pushes are over MaxScriptElementSize, which is exactly how such a
	// spend has always failed - the fork must not change pre-activation behaviour.
	err = executeMLDSA44(tx, scriptPubKey, ScriptNoFlags)
	if !IsErrorCode(err, ErrElementTooBig) {
		t.Fatalf("pre-activation ML-DSA-44 spend: want ErrElementTooBig, got %v", err)
	}
}

func TestMLDSA44OpcodeIsUnknownBeforeActivation(t *testing.T) {
	tx := newMinimalTestTxWith(nil, 0, 0)
	// Two small pushes then the opcode, so the only thing that can fail is the opcode itself.
	script, err := NewScriptBuilder().AddData([]byte{1}).AddData([]byte{2}).AddOp(OpCheckSigMLDSA44).Script()
	if err != nil {
		t.Fatalf("build script: %v", err)
	}
	err = executeMLDSA44(tx, &externalapi.ScriptPublicKey{Script: script}, ScriptNoFlags)
	if !IsErrorCode(err, ErrReservedOpcode) {
		t.Fatalf("0xa6 before activation: want ErrReservedOpcode, got %v", err)
	}

	// In an unexecuted branch it was always harmless, and still is.
	script, err = NewScriptBuilder().AddOp(Op0).AddOp(OpIf).AddOp(OpCheckSigMLDSA44).AddOp(OpEndIf).AddOp(Op1).Script()
	if err != nil {
		t.Fatalf("build script: %v", err)
	}
	if err := executeMLDSA44(tx, &externalapi.ScriptPublicKey{Script: script}, ScriptNoFlags); err != nil {
		t.Fatalf("0xa6 in an unexecuted branch before activation: %v", err)
	}
}

func TestMLDSA44RejectsBadSpends(t *testing.T) {
	publicKey, privateKey := mldsa44TestKey(t, 1)
	otherPublicKey, otherPrivateKey := mldsa44TestKey(t, 2)

	sign := func(tx *externalapi.DomainTransaction, key *mldsa44.PrivateKey) []byte {
		sig, err := RawTxInSignatureMLDSA44(tx, 0, consensushashing.SigHashAll, key, &consensushashing.SighashReusedValues{})
		if err != nil {
			t.Fatalf("RawTxInSignatureMLDSA44: %v", err)
		}
		return sig
	}
	sigScriptOf := func(sig, pubKey []byte) []byte {
		script, err := NewScriptBuilder().AddFullData(sig).AddFullData(pubKey).Script()
		if err != nil {
			t.Fatalf("build signature script: %v", err)
		}
		return script
	}

	tests := []struct {
		name      string
		sigScript func(tx *externalapi.DomainTransaction) []byte
		wantCode  ErrorCode
	}{
		{
			name: "key does not hash to the address",
			sigScript: func(tx *externalapi.DomainTransaction) []byte {
				return sigScriptOf(sign(tx, otherPrivateKey), otherPublicKey.Bytes())
			},
			wantCode: ErrEqualVerify,
		},
		{
			name: "signature by another key",
			sigScript: func(tx *externalapi.DomainTransaction) []byte {
				return sigScriptOf(sign(tx, otherPrivateKey), publicKey.Bytes())
			},
			wantCode: ErrNullFail,
		},
		{
			name: "flipped signature bit",
			sigScript: func(tx *externalapi.DomainTransaction) []byte {
				sig := sign(tx, privateKey)
				sig[100] ^= 1
				return sigScriptOf(sig, publicKey.Bytes())
			},
			wantCode: ErrNullFail,
		},
		{
			name: "Schnorr sighash instead of the ML-DSA-44 domain",
			sigScript: func(tx *externalapi.DomainTransaction) []byte {
				hash, err := consensushashing.CalculateSignatureHashSchnorr(tx, 0, consensushashing.SigHashAll,
					&consensushashing.SighashReusedValues{})
				if err != nil {
					t.Fatalf("CalculateSignatureHashSchnorr: %v", err)
				}
				sig := make([]byte, mldsa44.SignatureSize)
				if err := mldsa44.SignTo(privateKey, hash.ByteSlice(), nil, false, sig); err != nil {
					t.Fatalf("SignTo: %v", err)
				}
				return sigScriptOf(append(sig, byte(consensushashing.SigHashAll)), publicKey.Bytes())
			},
			wantCode: ErrNullFail,
		},
		{
			name: "invalid sighash type",
			sigScript: func(tx *externalapi.DomainTransaction) []byte {
				sig := sign(tx, privateKey)
				sig[len(sig)-1] = 0x7f
				return sigScriptOf(sig, publicKey.Bytes())
			},
			wantCode: ErrInvalidSigHashType,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tx, scriptPubKey := newMLDSA44SpendTx(t, publicKey)
			tx.Inputs[0].SignatureScript = test.sigScript(tx)
			err := executeMLDSA44(tx, scriptPubKey, ScriptEnableMLDSA44)
			if !IsErrorCode(err, test.wantCode) {
				t.Fatalf("want %v, got %v", test.wantCode, err)
			}
		})
	}
}

func TestMLDSA44SignatureCommitsToTransaction(t *testing.T) {
	publicKey, privateKey := mldsa44TestKey(t, 1)
	tx, scriptPubKey := newMLDSA44SpendTx(t, publicKey)
	sigScript, err := SignatureScriptMLDSA44(tx, 0, consensushashing.SigHashAll, privateKey, &consensushashing.SighashReusedValues{})
	if err != nil {
		t.Fatalf("SignatureScriptMLDSA44: %v", err)
	}
	tx.Inputs[0].SignatureScript = sigScript
	tx.Outputs[0].Value++

	err = executeMLDSA44(tx, scriptPubKey, ScriptEnableMLDSA44)
	if !IsErrorCode(err, ErrNullFail) {
		t.Fatalf("spend with a modified output: want ErrNullFail, got %v", err)
	}
}

func TestMLDSA44ExemptsOnlyItsOwnElementSizes(t *testing.T) {
	tx := newMinimalTestTxWith(nil, 0, 0)
	for _, size := range []int{MaxScriptElementSize + 1, mldsa44.PublicKeySize - 1, mldsa44.PublicKeySize + 1,
		mldsa44.SignatureSize, mldsa44.SignatureSize + 2} {
		script, err := NewScriptBuilder().AddFullData(bytes.Repeat([]byte{1}, size)).AddOp(OpDrop).AddOp(Op1).Script()
		if err != nil {
			t.Fatalf("build script: %v", err)
		}
		err = executeMLDSA44(tx, &externalapi.ScriptPublicKey{Script: script}, ScriptEnableMLDSA44)
		if !IsErrorCode(err, ErrElementTooBig) {
			t.Fatalf("%d-byte push under ScriptEnableMLDSA44: want ErrElementTooBig, got %v", size, err)
		}
	}
}

func TestMLDSA44SigOpCountIsGated(t *testing.T) {
	publicKey, _ := mldsa44TestKey(t, 1)
	_, scriptPubKey := newMLDSA44SpendTx(t, publicKey)

	if got := GetPreciseSigOpCount(nil, scriptPubKey); got != 0 {
		t.Fatalf("sigops before activation: got %d, want 0 (0xa6 has always counted as 0)", got)
	}
	if got := GetPreciseSigOpCountWithFlags(nil, scriptPubKey, ScriptEnableMLDSA44); got != 1 {
		t.Fatalf("sigops after activation: got %d, want 1", got)
	}

	// Through P2SH as well.
	p2sh, err := PayToScriptHashScript(scriptPubKey.Script)
	if err != nil {
		t.Fatalf("PayToScriptHashScript: %v", err)
	}
	sigScript, err := NewScriptBuilder().AddData(scriptPubKey.Script).Script()
	if err != nil {
		t.Fatalf("build signature script: %v", err)
	}
	p2shScriptPubKey := &externalapi.ScriptPublicKey{Script: p2sh}
	if got := GetPreciseSigOpCount(sigScript, p2shScriptPubKey); got != 0 {
		t.Fatalf("P2SH sigops before activation: got %d, want 0", got)
	}
	if got := GetPreciseSigOpCountWithFlags(sigScript, p2shScriptPubKey, ScriptEnableMLDSA44); got != 1 {
		t.Fatalf("P2SH sigops after activation: got %d, want 1", got)
	}
}

func TestMLDSA44AddressRoundTrip(t *testing.T) {
	publicKey, _ := mldsa44TestKey(t, 1)
	_, scriptPubKey := newMLDSA44SpendTx(t, publicKey)

	class, address, err := ExtractScriptPubKeyAddress(scriptPubKey, &dagconfig.MainnetParams)
	if err != nil {
		t.Fatalf("ExtractScriptPubKeyAddress: %v", err)
	}
	if class != PubKeyHashMLDSA44Ty {
		t.Fatalf("script class: got %s, want %s", class, PubKeyHashMLDSA44Ty)
	}
	decoded, err := util.DecodeAddress(address.EncodeAddress(), util.Bech32PrefixHoosat)
	if err != nil {
		t.Fatalf("DecodeAddress(%s): %v", address.EncodeAddress(), err)
	}
	if _, ok := decoded.(*util.AddressPublicKeyHashMLDSA44); !ok {
		t.Fatalf("decoded %s as %T", address.EncodeAddress(), decoded)
	}
	roundTripped, err := PayToAddrScript(decoded)
	if err != nil {
		t.Fatalf("PayToAddrScript: %v", err)
	}
	if !roundTripped.Equal(scriptPubKey) {
		t.Fatalf("script changed across address round trip")
	}
}

// TestMLDSA44InteropWithStandardLibrary pins that CIRCL, which consensus verifies with, agrees with
// Go's own FIPS 204 implementation on key generation and on signatures. A wallet built on either
// one must produce spends the other accepts.
func TestMLDSA44InteropWithStandardLibrary(t *testing.T) {
	var seed [mldsa44.SeedSize]byte
	for i := range seed {
		seed[i] = byte(i)
	}
	circlPublicKey, circlPrivateKey := mldsa44.NewKeyFromSeed(&seed)
	stdPrivateKey, err := stdmldsa.NewPrivateKey(stdmldsa.MLDSA44(), seed[:])
	if err != nil {
		t.Fatalf("crypto/mldsa NewPrivateKey: %v", err)
	}
	if !bytes.Equal(circlPublicKey.Bytes(), stdPrivateKey.PublicKey().Bytes()) {
		t.Fatalf("CIRCL and crypto/mldsa derive different public keys from the same seed")
	}

	message := []byte("hoosat ML-DSA-44 interop")
	stdSignature, err := stdPrivateKey.Sign(nil, message, nil)
	if err != nil {
		t.Fatalf("crypto/mldsa Sign: %v", err)
	}
	if !mldsa44.Verify(circlPublicKey, message, nil, stdSignature) {
		t.Fatalf("CIRCL rejects a crypto/mldsa signature")
	}

	circlSignature := make([]byte, mldsa44.SignatureSize)
	if err := mldsa44.SignTo(circlPrivateKey, message, nil, true, circlSignature); err != nil {
		t.Fatalf("CIRCL SignTo: %v", err)
	}
	if err := stdmldsa.Verify(stdPrivateKey.PublicKey(), message, circlSignature, nil); err != nil {
		t.Fatalf("crypto/mldsa rejects a CIRCL signature: %v", err)
	}
}
