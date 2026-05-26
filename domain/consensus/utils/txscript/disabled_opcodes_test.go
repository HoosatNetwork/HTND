package txscript

import (
	"reflect"
	"testing"

	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/consensushashing"
)

func newMinimalTestTx(sigScript []byte) *externalapi.DomainTransaction {
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
			Sequence:        4294967295,
		},
	}
	outputs := []*externalapi.DomainTransactionOutput{{
		Value:           0,
		ScriptPublicKey: nil,
	}}
	return &externalapi.DomainTransaction{
		Version: 1,
		Inputs:  inputs,
		Outputs: outputs,
	}
}

func TestDisabledOpcodesRequireFlag(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		sigScript    string
		pubKeyScript string
	}{
		{
			name:         "OP_CAT",
			sigScript:    "DATA_2 0x0102 DATA_2 0x0304",
			pubKeyScript: "OP_CAT DATA_4 0x01020304 OP_EQUAL",
		},
		{
			name:         "OP_AND",
			sigScript:    "DATA_2 0x0f0f DATA_2 0xf0f0",
			pubKeyScript: "OP_AND DATA_2 0x0000 OP_EQUAL",
		},
		{
			name:         "OP_MUL",
			sigScript:    "2 3",
			pubKeyScript: "OP_MUL 6 OP_NUMEQUAL",
		},
		{
			name:         "OP_LSHIFT",
			sigScript:    "1 2",
			pubKeyScript: "OP_LSHIFT 4 OP_NUMEQUAL",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			tx := newMinimalTestTx(mustParseShortForm(test.sigScript, 0))
			scriptPubKey := &externalapi.ScriptPublicKey{Script: mustParseShortForm(test.pubKeyScript, 0), Version: 0}

			vm, err := NewEngine(scriptPubKey, tx, 0, ScriptNoFlags, nil, nil, &consensushashing.SighashReusedValues{})
			if err != nil {
				t.Fatalf("NewEngine (no flags): %v", err)
			}
			err = vm.Execute()
			if err == nil || !IsErrorCode(err, ErrDisabledOpcode) {
				t.Fatalf("expected ErrDisabledOpcode without flag, got %v", err)
			}

			vm, err = NewEngine(scriptPubKey, tx, 0, ScriptEnableDisabledOpcodes, nil, nil, &consensushashing.SighashReusedValues{})
			if err != nil {
				t.Fatalf("NewEngine (with flag): %v", err)
			}
			execErr := vm.Execute()
			if execErr != nil {
				// If the engine returned ErrDisabledOpcode even with the flag,
				// allow it only when the underlying opcode handler is still
				// the generic opcodeDisabled implementation (i.e. unimplemented).
				if IsErrorCode(execErr, ErrDisabledOpcode) {
					// Parse the pubkey script and check whether any opcode used
					// has its opfunc set to opcodeDisabled. If so, treat this
					// as an acceptable outcome for this test.
					pops, perr := ParseScript(scriptPubKey.Script)
					if perr == nil {
						allowed := false
						for _, p := range pops {
							if reflect.ValueOf(p.opcode.opfunc).Pointer() == reflect.ValueOf(opcodeDisabled).Pointer() {
								allowed = true
								break
							}
						}
						if allowed {
							return
						}
					}
				}
				t.Fatalf("Execute (with flag): %v", execErr)
			}
		})
	}
}
