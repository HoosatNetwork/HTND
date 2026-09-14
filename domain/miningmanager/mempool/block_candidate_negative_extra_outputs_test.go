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

// TestBlockCandidateTransactionsWithMoreInputsThanOutputs pins that a ready transaction with three or more
// outputs and more inputs than outputs is offered as a block candidate. Its extra-output count is negative,
// and converting that count panicked, so once such a transaction was relayed into a mining node's mempool
// the node crashed on its next GetBlockTemplate.
func TestBlockCandidateTransactionsWithMoreInputsThanOutputs(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		tc, teardown, err := consensus.NewFactory().NewTestConsensus(consensusConfig, "TestBlockCandidateTransactionsWithMoreInputsThanOutputs")
		if err != nil {
			t.Fatalf("Error setting up consensus: %+v", err)
		}
		defer teardown(false)

		tcAsConsensus := tc.(externalapi.Consensus)
		tcAsConsensusPointer := &tcAsConsensus
		mp := New(DefaultConfig(tc.DAGParams()), consensusreference.NewConsensusReference(&tcAsConsensusPointer)).(*mempool)

		scriptPublicKey, redeemScript := testutils.OpTrueScript()
		signatureScript, err := txscript.PayToScriptHashSignatureScript(redeemScript, nil)
		if err != nil {
			t.Fatalf("PayToScriptHashSignatureScript: %+v", err)
		}

		const inputCount, outputCount = 4, 3
		const fundingValue = 1_000_000
		inputs := make([]*externalapi.DomainTransactionInput, inputCount)
		for i := range inputCount {
			// Distinct values give the funding transactions distinct IDs.
			funding := testutils.CreateTransactionWithOutput(fundingValue + uint64(i))
			if err := testutils.StageTransactionOutputsToVirtual(tc, funding, 0); err != nil {
				t.Fatalf("StageTransactionOutputsToVirtual: %+v", err)
			}
			inputs[i] = &externalapi.DomainTransactionInput{
				PreviousOutpoint: externalapi.DomainOutpoint{TransactionID: *consensushashing.TransactionID(funding)},
				SignatureScript:  signatureScript,
				Sequence:         constants.MaxTxInSequenceNum,
			}
		}
		outputs := make([]*externalapi.DomainTransactionOutput, outputCount)
		for i := range outputs {
			outputs[i] = &externalapi.DomainTransactionOutput{ScriptPublicKey: scriptPublicKey, Value: fundingValue}
		}
		transaction := &externalapi.DomainTransaction{
			Version: constants.MaxTransactionVersion,
			Inputs:  inputs,
			Outputs: outputs,
			Payload: []byte{},
		}
		if _, err := mp.ValidateAndInsertTransaction(transaction, false, false, true); err != nil {
			t.Fatalf("ValidateAndInsertTransaction: %+v", err)
		}

		candidates := mp.BlockCandidateTransactions()
		transactionID := consensushashing.TransactionID(transaction)
		for _, candidate := range candidates {
			if consensushashing.TransactionID(candidate).Equal(transactionID) {
				return
			}
		}
		t.Fatalf("expected transaction %s among the %d block candidates", transactionID, len(candidates))
	})
}
