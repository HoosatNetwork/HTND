package mempool

import (
	"testing"

	"github.com/Hoosat-Oy/HTND/domain/consensus"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/consensushashing"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/constants"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/testutils"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/txscript"
	"github.com/Hoosat-Oy/HTND/domain/consensusreference"
	"github.com/pkg/errors"
)

func TestValidateAndInsertTransactionReplacement(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		factory := consensus.NewFactory()
		tc, teardown, err := factory.NewTestConsensus(consensusConfig, "TestValidateAndInsertTransactionReplacement")
		if err != nil {
			t.Fatalf("Error setting up consensus: %+v", err)
		}
		defer teardown(false)

		tcAsConsensus := tc.(externalapi.Consensus)
		tcAsConsensusPointer := &tcAsConsensus

		newMempool := func() *mempool {
			mempoolConfig := DefaultConfig(tc.DAGParams())
			return New(mempoolConfig, consensusreference.NewConsensusReference(&tcAsConsensusPointer)).(*mempool)
		}

		t.Run("accepts replacement and evicts descendants", func(t *testing.T) {
			mp := newMempool()

			bootstrapTx := testutils.CreateTransactionWithOutput(10_000)
			if err := testutils.StageTransactionOutputsToVirtual(tc, bootstrapTx, 0); err != nil {
				t.Fatalf("StageTransactionOutputsToVirtual: %+v", err)
			}

			conflictTx, err := testutils.CreateTransaction(bootstrapTx, 1_000)
			if err != nil {
				t.Fatalf("CreateTransaction(conflictTx): %+v", err)
			}
			if _, err := mp.ValidateAndInsertTransaction(conflictTx, true, false); err != nil {
				t.Fatalf("ValidateAndInsertTransaction(conflictTx): %+v", err)
			}

			descendantTx, err := testutils.CreateTransaction(conflictTx, 1_000)
			if err != nil {
				t.Fatalf("CreateTransaction(descendantTx): %+v", err)
			}
			if _, err := mp.ValidateAndInsertTransaction(descendantTx, true, false); err != nil {
				t.Fatalf("ValidateAndInsertTransaction(descendantTx): %+v", err)
			}

			replacementTx, err := testutils.CreateTransaction(bootstrapTx, 2_500)
			if err != nil {
				t.Fatalf("CreateTransaction(replacementTx): %+v", err)
			}
			accepted, replaced, err := mp.ValidateAndInsertTransactionReplacement(replacementTx, true)
			if err != nil {
				t.Fatalf("ValidateAndInsertTransactionReplacement: %+v", err)
			}
			if len(accepted) != 1 {
				t.Fatalf("expected 1 accepted tx (replacement), got %d", len(accepted))
			}
			if replaced == nil {
				t.Fatalf("expected non-nil replaced transaction")
			}
			if consensushashing.TransactionID(replaced).String() != consensushashing.TransactionID(conflictTx).String() {
				t.Fatalf("expected replaced txid %s, got %s",
					consensushashing.TransactionID(conflictTx), consensushashing.TransactionID(replaced))
			}

			conflictID := consensushashing.TransactionID(conflictTx)
			_, _, found := mp.GetTransaction(conflictID, true, false)
			if found {
				t.Fatalf("expected conflict tx to be evicted")
			}

			descendantID := consensushashing.TransactionID(descendantTx)
			_, _, found = mp.GetTransaction(descendantID, true, false)
			if found {
				t.Fatalf("expected descendant tx to be evicted")
			}

			replacementID := consensushashing.TransactionID(replacementTx)
			_, _, found = mp.GetTransaction(replacementID, true, false)
			if !found {
				t.Fatalf("expected replacement tx to be in the mempool")
			}
		})

		t.Run("rejects replacement with insufficient fee", func(t *testing.T) {
			mp := newMempool()

			bootstrapTx := testutils.CreateTransactionWithOutput(10_001)
			if err := testutils.StageTransactionOutputsToVirtual(tc, bootstrapTx, 0); err != nil {
				t.Fatalf("StageTransactionOutputsToVirtual: %+v", err)
			}

			conflictTx, err := testutils.CreateTransaction(bootstrapTx, 1_000)
			if err != nil {
				t.Fatalf("CreateTransaction(conflictTx): %+v", err)
			}
			if _, err := mp.ValidateAndInsertTransaction(conflictTx, true, false); err != nil {
				t.Fatalf("ValidateAndInsertTransaction(conflictTx): %+v", err)
			}

			descendantTx, err := testutils.CreateTransaction(conflictTx, 1_000)
			if err != nil {
				t.Fatalf("CreateTransaction(descendantTx): %+v", err)
			}
			if _, err := mp.ValidateAndInsertTransaction(descendantTx, true, false); err != nil {
				t.Fatalf("ValidateAndInsertTransaction(descendantTx): %+v", err)
			}

			// Total removed fee is 2_000. Replacement fee must be strictly higher.
			replacementTx, err := testutils.CreateTransaction(bootstrapTx, 2_000)
			if err != nil {
				t.Fatalf("CreateTransaction(replacementTx): %+v", err)
			}

			_, _, err = mp.ValidateAndInsertTransactionReplacement(replacementTx, true)
			if err == nil {
				t.Fatalf("expected error")
			}

			var txRuleErr TxRuleError
			if !errors.As(err, &txRuleErr) {
				t.Fatalf("expected TxRuleError, got %T (%v)", err, err)
			}
			if txRuleErr.RejectCode != RejectInsufficientFee {
				t.Fatalf("expected reject code %s, got %s", RejectInsufficientFee, txRuleErr.RejectCode)
			}
		})

		t.Run("rejects replacement with insufficient fee rate", func(t *testing.T) {
			mp := newMempool()

			// Create enough UTXOs so the replacement transaction can have many inputs,
			// inflating its mass and thus lowering its fee-rate.
			bootstrapTx := testutils.CreateTransactionWithOutput(1_000_000)
			if err := testutils.StageTransactionOutputsToVirtual(tc, bootstrapTx, 0); err != nil {
				t.Fatalf("StageTransactionOutputsToVirtual: %+v", err)
			}
			additionalInputsCount := 49
			additionalBootstraps := make([]*externalapi.DomainTransaction, 0, additionalInputsCount)
			for i := 0; i < additionalInputsCount; i++ {
				tx := testutils.CreateTransactionWithOutput(1_000_000 + uint64(i+1))
				if err := testutils.StageTransactionOutputsToVirtual(tc, tx, 0); err != nil {
					t.Fatalf("StageTransactionOutputsToVirtual(additional): %+v", err)
				}
				additionalBootstraps = append(additionalBootstraps, tx)
			}

			conflictTx, err := testutils.CreateTransaction(bootstrapTx, 200_000)
			if err != nil {
				t.Fatalf("CreateTransaction(conflictTx): %+v", err)
			}
			if _, err := mp.ValidateAndInsertTransaction(conflictTx, true, false); err != nil {
				t.Fatalf("ValidateAndInsertTransaction(conflictTx): %+v", err)
			}

			descendantTx, err := testutils.CreateTransaction(conflictTx, 200_000)
			if err != nil {
				t.Fatalf("CreateTransaction(descendantTx): %+v", err)
			}
			if _, err := mp.ValidateAndInsertTransaction(descendantTx, true, false); err != nil {
				t.Fatalf("ValidateAndInsertTransaction(descendantTx): %+v", err)
			}

			// Total removed fee is 400_000. Make fee slightly higher, but inflate mass with extra inputs.
			scriptPublicKey, redeemScript := testutils.OpTrueScript()
			signatureScript, err := txscript.PayToScriptHashSignatureScript(redeemScript, nil)
			if err != nil {
				t.Fatalf("PayToScriptHashSignatureScript: %+v", err)
			}

			inputs := make([]*externalapi.DomainTransactionInput, 0, 1+additionalInputsCount)
			inputs = append(inputs, &externalapi.DomainTransactionInput{
				PreviousOutpoint: externalapi.DomainOutpoint{TransactionID: *consensushashing.TransactionID(bootstrapTx), Index: 0},
				SignatureScript:  signatureScript,
				Sequence:         constants.MaxTxInSequenceNum,
			})
			for _, tx := range additionalBootstraps {
				inputs = append(inputs, &externalapi.DomainTransactionInput{
					PreviousOutpoint: externalapi.DomainOutpoint{TransactionID: *consensushashing.TransactionID(tx), Index: 0},
					SignatureScript:  signatureScript,
					Sequence:         constants.MaxTxInSequenceNum,
				})
			}

			// Total input value is 10 * 1_000_000 (plus a few extra sompi due to unique values).
			// Choose a fee slightly higher than the removed set.
			fee := uint64(400_001)
			totalInputValue := uint64(0)
			totalInputValue += bootstrapTx.Outputs[0].Value
			for _, tx := range additionalBootstraps {
				totalInputValue += tx.Outputs[0].Value
			}
			replacementTx := &externalapi.DomainTransaction{
				Version: constants.MaxTransactionVersion,
				Inputs:  inputs,
				Outputs: []*externalapi.DomainTransactionOutput{{
					ScriptPublicKey: scriptPublicKey,
					Value:           totalInputValue - fee,
				}},
				Payload: []byte{},
			}

			_, _, err = mp.ValidateAndInsertTransactionReplacement(replacementTx, true)
			if err == nil {
				t.Fatalf("expected error")
			}

			var txRuleErr TxRuleError
			if !errors.As(err, &txRuleErr) {
				t.Fatalf("expected TxRuleError, got %T (%v)", err, err)
			}
			if txRuleErr.RejectCode != RejectInsufficientFee {
				t.Fatalf("expected reject code %s, got %s", RejectInsufficientFee, txRuleErr.RejectCode)
			}
		})
	})
}
