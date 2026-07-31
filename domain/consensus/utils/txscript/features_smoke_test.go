package txscript

import (
	"crypto/sha256"
	"testing"

	"golang.org/x/crypto/blake2b"

	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/constants"
)

func newMinimalTestTxWith(sigScript []byte, lockTime uint64, sequence uint64) *externalapi.DomainTransaction {
	inputs := []*externalapi.DomainTransactionInput{
		{
			PreviousOutpoint: externalapi.DomainOutpoint{
				TransactionID: *externalapi.NewDomainTransactionIDFromByteArray(&[externalapi.DomainHashSize]byte{
					0xc9, 0x97, 0xa5, 0xe5,
					0x6e, 0x10, 0x41, 0x02,
					0xfa, 0x20, 0x9c, 0x6a,
					0x85, 0x2d, 0xd9, 0x06,
					0x60, 0xa2, 0x0b, 0x2d,
					0x9c, 0x35, 0x24, 0x23,
					0xed, 0xce, 0x25, 0x85,
					0x7f, 0xcd, 0x37, 0x04,
				}),
				Index: 0,
			},
			SignatureScript: sigScript,
			Sequence:        sequence,
		},
	}
	outputs := []*externalapi.DomainTransactionOutput{{
		Value:           0,
		ScriptPublicKey: nil,
	}}
	return &externalapi.DomainTransaction{
		Version:  1,
		Inputs:   inputs,
		Outputs:  outputs,
		LockTime: lockTime,
	}
}

func TestFeaturesSmoke(t *testing.T) {
	t.Parallel()

	t.Run("IF_ELSE", func(t *testing.T) {
		t.Parallel()

		tx := newMinimalTestTxWith(nil, 0, constants.MaxTxInSequenceNum)
		conditionalScript, err := NewScriptBuilder().
			AddInt64(1).
			AddOp(OpIf).
			AddInt64(1).
			AddOp(OpElse).
			AddInt64(0).
			AddOp(OpEndIf).
			Script()
		if err != nil {
			t.Fatalf("build conditional script: %v", err)
		}
		scriptPubKey := &externalapi.ScriptPublicKey{Script: conditionalScript, Version: 0}
		vm, err := NewEngine(scriptPubKey, tx, 0, ScriptNoFlags, nil, nil, &consensushashing.SighashReusedValues{})
		if err != nil {
			t.Fatalf("NewEngine: %v", err)
		}
		if err := vm.Execute(); err != nil {
			t.Fatalf("Execute: %v", err)
		}
	})

	t.Run("CLTV_success", func(t *testing.T) {
		t.Parallel()

		// CLTV fails if the input is finalized, so use a non-max sequence.
		tx := newMinimalTestTxWith(nil, 10, 0)
		scriptPubKey := &externalapi.ScriptPublicKey{Script: mustParseShortForm("5 CHECKLOCKTIMEVERIFY 1", 0), Version: 0}
		vm, err := NewEngine(scriptPubKey, tx, 0, ScriptNoFlags, nil, nil, &consensushashing.SighashReusedValues{})
		if err != nil {
			t.Fatalf("NewEngine: %v", err)
		}
		if err := vm.Execute(); err != nil {
			t.Fatalf("Execute: %v", err)
		}
	})

	t.Run("CLTV_failure", func(t *testing.T) {
		t.Parallel()

		tx := newMinimalTestTxWith(nil, 10, 0)
		scriptPubKey := &externalapi.ScriptPublicKey{Script: mustParseShortForm("50 CHECKLOCKTIMEVERIFY 1", 0), Version: 0}
		vm, err := NewEngine(scriptPubKey, tx, 0, ScriptNoFlags, nil, nil, &consensushashing.SighashReusedValues{})
		if err != nil {
			t.Fatalf("NewEngine: %v", err)
		}
		err = vm.Execute()
		if err == nil || !IsErrorCode(err, ErrUnsatisfiedLockTime) {
			t.Fatalf("expected ErrUnsatisfiedLockTime, got %v", err)
		}
	})

	t.Run("CSV_success", func(t *testing.T) {
		t.Parallel()

		tx := newMinimalTestTxWith(nil, 0, 10)
		scriptPubKey := &externalapi.ScriptPublicKey{Script: mustParseShortForm("5 CHECKSEQUENCEVERIFY 1", 0), Version: 0}
		vm, err := NewEngine(scriptPubKey, tx, 0, ScriptNoFlags, nil, nil, &consensushashing.SighashReusedValues{})
		if err != nil {
			t.Fatalf("NewEngine: %v", err)
		}
		if err := vm.Execute(); err != nil {
			t.Fatalf("Execute: %v", err)
		}
	})

	t.Run("Hashlock_sha256", func(t *testing.T) {
		t.Parallel()

		preimage := []byte{0x01, 0x02, 0x03}
		h := sha256.Sum256(preimage)

		sigScript, err := NewScriptBuilder().AddData(preimage).Script()
		if err != nil {
			t.Fatalf("build sigScript: %v", err)
		}
		// OP_SHA256 <hash> OP_EQUAL
		scriptPubKey := &externalapi.ScriptPublicKey{Script: mustParseShortForm("SHA256 DATA_32 0x"+byteArrayToHex(h[:])+" EQUAL", 0), Version: 0}
		tx := newMinimalTestTxWith(sigScript, 0, constants.MaxTxInSequenceNum)

		vm, err := NewEngine(scriptPubKey, tx, 0, ScriptNoFlags, nil, nil, &consensushashing.SighashReusedValues{})
		if err != nil {
			t.Fatalf("NewEngine: %v", err)
		}
		if err := vm.Execute(); err != nil {
			t.Fatalf("Execute: %v", err)
		}
	})

	t.Run("P2SH_redeemScript_exec", func(t *testing.T) {
		t.Parallel()

		redeemScript := mustParseShortForm("1", 0)
		redeemHash := blake2b.Sum256(redeemScript)

		sigScript, err := NewScriptBuilder().AddData(redeemScript).Script()
		if err != nil {
			t.Fatalf("build sigScript: %v", err)
		}
		scriptPubKey := &externalapi.ScriptPublicKey{Script: mustParseShortForm("BLAKE2B DATA_32 0x"+byteArrayToHex(redeemHash[:])+" EQUAL", 0), Version: 0}
		tx := newMinimalTestTxWith(sigScript, 0, constants.MaxTxInSequenceNum)

		vm, err := NewEngine(scriptPubKey, tx, 0, ScriptNoFlags, nil, nil, &consensushashing.SighashReusedValues{})
		if err != nil {
			t.Fatalf("NewEngine: %v", err)
		}
		if err := vm.Execute(); err != nil {
			t.Fatalf("Execute: %v", err)
		}
	})
}

func byteArrayToHex(b []byte) string {
	const hex = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2] = hex[v>>4]
		out[i*2+1] = hex[v&0x0f]
	}
	return string(out)
}
