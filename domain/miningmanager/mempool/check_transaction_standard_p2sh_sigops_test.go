package mempool

import (
	"strings"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/txscript"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/utxo"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
)

// p2shSpendWithSigOps returns a transaction spending a P2SH output whose redeem script holds
// sigOps copies of opcode, with a fee well above the minimum so only sigop standardness is judged.
func p2shSpendWithSigOps(t *testing.T, opcode byte, sigOps int) *externalapi.DomainTransaction {
	t.Helper()
	redeemScript := make([]byte, sigOps)
	for i := range redeemScript {
		redeemScript[i] = opcode
	}
	scriptPubKey, err := txscript.PayToScriptHashScript(redeemScript)
	if err != nil {
		t.Fatalf("PayToScriptHashScript: %+v", err)
	}
	signatureScript, err := txscript.NewScriptBuilder().AddData(redeemScript).Script()
	if err != nil {
		t.Fatalf("Script: %+v", err)
	}
	tx := transactionSpending(utxo.NewUTXOEntry(1_000_000, &externalapi.ScriptPublicKey{Script: scriptPubKey}, false, 0))
	tx.Inputs[0].SignatureScript = signatureScript
	tx.StoreFee(1_000_000)
	return tx
}

// TestCheckTransactionStandardP2SHSigOpsMLDSA44 pins the 15/16 boundary of maxStandardP2SHSigOps
// around ML-DSA-44 activation. OP_CHECKSIGMLDSA44 counts only from the virtual DAA score at which
// consensus activates it (the same rule transaction admission applies); OP_CHECKSIG always counts.
func TestCheckTransactionStandardP2SHSigOpsMLDSA44(t *testing.T) {
	params := &dagconfig.MainnetParams
	activation := params.POWScores[params.HardForkGates.MLDSA44SignaturesBlockVersion-2]

	tests := []struct {
		name    string
		virtual uint64
		opcode  byte
		sigOps  int
		wantErr bool
	}{
		{"checksig 15 before activation", activation - 1, txscript.OpCheckSig, 15, false},
		{"checksig 16 before activation", activation - 1, txscript.OpCheckSig, 16, true},
		{"checksig 15 after activation", activation, txscript.OpCheckSig, 15, false},
		{"checksig 16 after activation", activation, txscript.OpCheckSig, 16, true},
		{"mldsa 16 before activation is not counted", activation - 1, txscript.OpCheckSigMLDSA44, 16, false},
		{"mldsa 15 after activation", activation, txscript.OpCheckSigMLDSA44, 15, false},
		{"mldsa 16 after activation is counted", activation, txscript.OpCheckSigMLDSA44, 16, true},
		{"mldsa 16 after activation, well past", activation + 1_000_000, txscript.OpCheckSigMLDSA44, 16, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mp := newMempoolAtVirtualDAAScore(test.virtual)
			err := mp.checkTransactionStandardInContext(p2shSpendWithSigOps(t, test.opcode, test.sigOps))
			if !test.wantErr {
				if err != nil {
					t.Fatalf("expected the transaction to be standard, got: %+v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "signature operations") {
				t.Fatalf("expected a sigop standardness rejection, got: %+v", err)
			}
			if code, found := extractRejectCode(err); !found || code != RejectNonstandard {
				t.Fatalf("expected RejectNonstandard, got %v (found %t)", code, found)
			}
		})
	}
}
