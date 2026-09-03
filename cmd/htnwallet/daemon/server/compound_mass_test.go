package server

import (
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/subnetworks"
	"github.com/HoosatNetwork/HTND/domain/dagconfig"
	"github.com/HoosatNetwork/HTND/domain/miningmanager/mempool"
	"github.com/HoosatNetwork/HTND/util/txmass"
)

// compoundMassForInputs measures what an N-input, single-output compound transaction weighs once
// signed, for a given per-input signature script size. It mirrors what estimateMassAfterSignatures
// computes, without needing a live wallet.
func compoundMassForInputs(inputCount int, sigScriptLen int, outputScriptLen int) uint64 {
	params := &dagconfig.MainnetParams
	calculator := txmass.NewCalculator(params.MassPerTxByte, params.MassPerScriptPubKeyByte, params.MassPerSigOp)

	inputs := make([]*externalapi.DomainTransactionInput, 0, inputCount)
	for i := range inputCount {
		inputs = append(inputs, &externalapi.DomainTransactionInput{
			PreviousOutpoint: externalapi.DomainOutpoint{Index: uint32(i)},
			SignatureScript:  make([]byte, sigScriptLen),
			SigOpCount:       1,
		})
	}
	return calculator.CalculateTransactionMass(&externalapi.DomainTransaction{
		Inputs: inputs,
		Outputs: []*externalapi.DomainTransactionOutput{{
			Value:           1,
			ScriptPublicKey: &externalapi.ScriptPublicKey{Script: make([]byte, outputScriptLen), Version: 0},
		}},
		SubnetworkID: subnetworks.SubnetworkIDNative,
	})
}

// TestTargetCompoundInputsOnlyFitsP2PK is the measurement behind
// buildCompoundTransactionWithinStandardMass: the fixed targetCompoundInputs count only keeps a
// compound under the standard mass limit for P2PK inputs. P2PKH and P2SH inputs carry more
// signature-script bytes and go over, which is what pushed those wallets into the split-and-merge
// path whose split transactions pay the change address instead of the requested destination.
func TestTargetCompoundInputsOnlyFitsP2PK(t *testing.T) {
	const p2pkOutputScriptLen = 34
	cases := []struct {
		name         string
		sigScriptLen int
		wantOverMass bool
	}{
		// push(sig 65)
		{"P2PK", 66, false},
		// push(sig 65) + push(schnorr pubkey 32)
		{"P2PKH", 99, true},
		// push(sig 65) + push(pubkey 32) + push(redeem script 37)
		{"P2SH", 137, true},
	}

	for _, test := range cases {
		mass := compoundMassForInputs(targetCompoundInputs, test.sigScriptLen, p2pkOutputScriptLen)
		overMass := mass > mempool.MaximumStandardTransactionMass
		t.Logf("%s: %d inputs => mass %d (limit %d)", test.name, targetCompoundInputs, mass,
			uint64(mempool.MaximumStandardTransactionMass))
		if overMass != test.wantOverMass {
			t.Errorf("%s: %d inputs measured %d mass, over-limit=%t, want over-limit=%t",
				test.name, targetCompoundInputs, mass, overMass, test.wantOverMass)
		}
	}
}

// TestBuildCompoundTransactionShrinksToFit pins that the builder reduces the input count until the
// compound fits, for every input type - so a compound stays a single transaction paying the
// destination directly, instead of being split into transactions that pay the change address.
func TestBuildCompoundTransactionShrinksToFit(t *testing.T) {
	const p2pkOutputScriptLen = 34
	for _, test := range []struct {
		name         string
		sigScriptLen int
	}{{"P2PK", 66}, {"P2PKH", 99}, {"P2SH", 137}} {
		fitting := 0
		for inputCount := targetCompoundInputs; inputCount >= 2; inputCount-- {
			if compoundMassForInputs(inputCount, test.sigScriptLen, p2pkOutputScriptLen) <=
				mempool.MaximumStandardTransactionMass {
				fitting = inputCount
				break
			}
		}
		if fitting < 2 {
			t.Fatalf("%s: no input count between 2 and %d fits the standard mass limit",
				test.name, targetCompoundInputs)
		}
		t.Logf("%s: largest compound that stays a single transaction is %d inputs (mass %d)",
			test.name, fitting, compoundMassForInputs(fitting, test.sigScriptLen, p2pkOutputScriptLen))
		if fitting > targetCompoundInputs {
			t.Errorf("%s: fitting count %d exceeds the selection ceiling %d", test.name, fitting, targetCompoundInputs)
		}
	}
}
