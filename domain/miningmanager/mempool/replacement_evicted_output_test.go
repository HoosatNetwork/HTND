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

// TestReplacementCannotSpendEvictedOutput pins that a replacement is rejected when it spends an output
// of a transaction the replacement itself would evict. Such a transaction can never be valid: once
// the eviction happens, the output it spends no longer exists anywhere.
func TestReplacementCannotSpendEvictedOutput(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		tc, teardown, err := consensus.NewFactory().NewTestConsensus(consensusConfig, "TestReplacementCannotSpendEvictedOutput")
		if err != nil {
			t.Fatalf("Error setting up consensus: %+v", err)
		}
		defer teardown(false)

		tcAsConsensus := tc.(externalapi.Consensus)
		tcAsConsensusPointer := &tcAsConsensus
		mp := New(DefaultConfig(tc.DAGParams()), consensusreference.NewConsensusReference(&tcAsConsensusPointer)).(*mempool)

		funding := testutils.CreateTransactionWithOutput(100_000)
		if err := testutils.StageTransactionOutputsToVirtual(tc, funding, 0); err != nil {
			t.Fatalf("StageTransactionOutputsToVirtual: %+v", err)
		}
		conflict, err := testutils.CreateTransaction(funding, 1_000)
		if err != nil {
			t.Fatalf("CreateTransaction(conflict): %+v", err)
		}
		redeemer, err := testutils.CreateTransaction(conflict, 1_000)
		if err != nil {
			t.Fatalf("CreateTransaction(redeemer): %+v", err)
		}
		if err := testutils.StageCreatedOutputsToVirtual(tc, conflict, 0); err != nil {
			t.Fatalf("StageCreatedOutputsToVirtual(conflict): %+v", err)
		}
		for _, transaction := range []*externalapi.DomainTransaction{conflict, redeemer} {
			if _, err := mp.ValidateAndInsertTransaction(transaction, true, false, true); err != nil {
				t.Fatalf("ValidateAndInsertTransaction: %+v", err)
			}
		}

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
		// replacement conflicts with conflict on the funding output, and also spends the output of
		// redeemer, which evicting conflict takes with it.
		replacement := &externalapi.DomainTransaction{
			Version: constants.MaxTransactionVersion,
			Inputs:  []*externalapi.DomainTransactionInput{newInput(funding), newInput(redeemer)},
			Outputs: []*externalapi.DomainTransactionOutput{{
				ScriptPublicKey: scriptPublicKey,
				Value:           funding.Outputs[0].Value + redeemer.Outputs[0].Value - 20_000,
			}},
			Payload: []byte{},
		}

		_, _, err = mp.ValidateAndInsertTransactionReplacement(replacement, true)
		if err == nil {
			t.Fatalf("replacement spending an output of a transaction it evicts should be rejected")
		}
		if _, ok := mp.transactionsPool.allTransactions[*consensushashing.TransactionID(replacement)]; ok {
			t.Fatalf("rejected replacement is in the mempool")
		}
		for _, transaction := range []*externalapi.DomainTransaction{conflict, redeemer} {
			if _, ok := mp.transactionsPool.allTransactions[*consensushashing.TransactionID(transaction)]; !ok {
				t.Fatalf("a rejected replacement must not evict anything")
			}
		}
	})
}
