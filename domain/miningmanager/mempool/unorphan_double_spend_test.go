package mempool

import (
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/testutils"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/txscript"
	"github.com/HoosatNetwork/HTND/domain/consensusreference"
)

// TestUnorphanRejectsDoubleSpendOfPoolTransaction pins that an orphan is not promoted into the mempool
// when one of its inputs is already spent by a transaction that entered the pool while it waited.
// Promotion used to skip the mempool double-spend check, leaving two pool transactions spending the
// same outpoint.
func TestUnorphanRejectsDoubleSpendOfPoolTransaction(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		tc, teardown, err := consensus.NewFactory().NewTestConsensus(consensusConfig, "TestUnorphanRejectsDoubleSpendOfPoolTransaction")
		if err != nil {
			t.Fatalf("Error setting up consensus: %+v", err)
		}
		defer teardown(false)

		tcAsConsensus := tc.(externalapi.Consensus)
		tcAsConsensusPointer := &tcAsConsensus
		mp := New(DefaultConfig(tc.DAGParams()), consensusreference.NewConsensusReference(&tcAsConsensusPointer)).(*mempool)

		// Distinct values: the helper is deterministic, so equal values would make these the same transaction.
		grandparent := testutils.CreateTransactionWithOutput(100_000)
		funding := testutils.CreateTransactionWithOutput(200_000)
		for _, transaction := range []*externalapi.DomainTransaction{grandparent, funding} {
			if err := testutils.StageTransactionOutputsToVirtual(tc, transaction, 0); err != nil {
				t.Fatalf("StageTransactionOutputsToVirtual: %+v", err)
			}
		}

		parent, err := testutils.CreateTransaction(grandparent, 1_000)
		if err != nil {
			t.Fatalf("CreateTransaction(parent): %+v", err)
		}
		conflicting, err := testutils.CreateTransaction(funding, 1_000)
		if err != nil {
			t.Fatalf("CreateTransaction(conflicting): %+v", err)
		}

		// orphan spends the parent's output, which is not known yet, and the funding output that
		// conflicting also spends.
		scriptPublicKey, redeemScript := testutils.OpTrueScript()
		signatureScript, err := txscript.PayToScriptHashSignatureScript(redeemScript, nil)
		if err != nil {
			t.Fatalf("PayToScriptHashSignatureScript: %+v", err)
		}
		newInput := func(spent *externalapi.DomainTransaction) *externalapi.DomainTransactionInput {
			return &externalapi.DomainTransactionInput{
				PreviousOutpoint: externalapi.DomainOutpoint{TransactionID: *consensushashing.TransactionID(spent)},
				SignatureScript:  signatureScript,
				Sequence:         constants.MaxTxInSequenceNum,
			}
		}
		orphan := &externalapi.DomainTransaction{
			Version: constants.MaxTransactionVersion,
			Inputs:  []*externalapi.DomainTransactionInput{newInput(parent), newInput(funding)},
			Outputs: []*externalapi.DomainTransactionOutput{{
				ScriptPublicKey: scriptPublicKey,
				Value:           parent.Outputs[0].Value + funding.Outputs[0].Value - 2_000,
			}},
			Payload: []byte{},
		}

		if accepted, err := mp.ValidateAndInsertTransaction(orphan, false, true, false); err != nil || len(accepted) != 0 {
			t.Fatalf("expected orphan to wait in the orphan pool, got %d accepted, err %+v", len(accepted), err)
		}
		if accepted, err := mp.ValidateAndInsertTransaction(conflicting, false, true, false); err != nil || len(accepted) != 1 {
			t.Fatalf("expected conflicting transaction to be accepted, got %d accepted, err %+v", len(accepted), err)
		}
		accepted, err := mp.ValidateAndInsertTransaction(parent, false, true, false)
		if err != nil {
			t.Fatalf("ValidateAndInsertTransaction(parent): %+v", err)
		}

		orphanID := consensushashing.TransactionID(orphan)
		if _, ok := mp.transactionsPool.allTransactions[*orphanID]; ok {
			t.Fatalf("orphan double-spending a pool transaction was promoted into the mempool (%d accepted)", len(accepted))
		}
		fundingOutpoint := externalapi.DomainOutpoint{TransactionID: *consensushashing.TransactionID(funding)}
		spender, ok := mp.mempoolUTXOSet.transactionByPreviousOutpoint[fundingOutpoint]
		if !ok || !spender.TransactionID().Equal(consensushashing.TransactionID(conflicting)) {
			t.Fatalf("the funding outpoint should still be recorded as spent by the conflicting transaction")
		}
	})
}
