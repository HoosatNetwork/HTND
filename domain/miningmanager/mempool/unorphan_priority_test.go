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

// TestUnorphanCarriesThePriorityTheOrphanEarned is HTN-146's regression test.
//
// A compound transaction relayed before its parent is raised to high priority so it survives while it
// waits in the orphan pool (raisePriorityIfCompound runs before maybeAddOrphan). unorphanTransaction
// used to promote it into the mempool proper with isHighPriority hardcoded to false, so the exact
// protection that kept it alive while orphaned was dropped the moment its parent arrived and it was
// promoted - a compound transaction could then expire or be evicted right after becoming eligible to
// be mined.
func TestUnorphanCarriesThePriorityTheOrphanEarned(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		tc, teardown, err := consensus.NewFactory().NewTestConsensus(consensusConfig, "TestUnorphanCarriesThePriorityTheOrphanEarned")
		if err != nil {
			t.Fatalf("Error setting up consensus: %+v", err)
		}
		defer teardown(false)

		tcAsConsensus := tc.(externalapi.Consensus)
		tcAsConsensusPointer := &tcAsConsensus
		config := DefaultConfig(tc.DAGParams())
		config.CompoundTxMinInputsThreshold = 2
		mp := New(config, consensusreference.NewConsensusReference(&tcAsConsensusPointer)).(*mempool)

		grandparent := testutils.CreateTransactionWithOutput(200_000)
		if err := testutils.StageTransactionOutputsToVirtual(tc, grandparent, 0); err != nil {
			t.Fatalf("StageTransactionOutputsToVirtual: %+v", err)
		}

		// parent has two outputs, so orphan (below) can spend both of them and reach the
		// CompoundTxMinInputsThreshold of 2.
		parent, err := testutils.CreateTransaction(grandparent, 2_000)
		if err != nil {
			t.Fatalf("CreateTransaction(parent): %+v", err)
		}
		half := parent.Outputs[0].Value / 2
		parent.Outputs = []*externalapi.DomainTransactionOutput{
			{ScriptPublicKey: parent.Outputs[0].ScriptPublicKey, Value: half},
			{ScriptPublicKey: parent.Outputs[0].ScriptPublicKey, Value: half},
		}

		scriptPublicKey, redeemScript := testutils.OpTrueScript()
		signatureScript, err := txscript.PayToScriptHashSignatureScript(redeemScript, nil)
		if err != nil {
			t.Fatalf("PayToScriptHashSignatureScript: %+v", err)
		}
		parentID := consensushashing.TransactionID(parent)
		orphan := &externalapi.DomainTransaction{
			Version: constants.MaxTransactionVersion,
			Inputs: []*externalapi.DomainTransactionInput{
				{
					PreviousOutpoint: externalapi.DomainOutpoint{TransactionID: *parentID, Index: 0},
					SignatureScript:  signatureScript,
					Sequence:         constants.MaxTxInSequenceNum,
				},
				{
					PreviousOutpoint: externalapi.DomainOutpoint{TransactionID: *parentID, Index: 1},
					SignatureScript:  signatureScript,
					Sequence:         constants.MaxTxInSequenceNum,
				},
			},
			Outputs: []*externalapi.DomainTransactionOutput{{
				ScriptPublicKey: scriptPublicKey,
				Value:           2*half - 1_000,
			}},
			Payload: []byte{},
		}

		// Relayed (isLocalSubmission=false, isHighPriority=false going in): raisePriorityIfCompound must
		// raise it on its own merit before it waits in the orphan pool.
		if accepted, err := mp.ValidateAndInsertTransaction(orphan, false, true, false); err != nil || len(accepted) != 0 {
			t.Fatalf("expected orphan to wait in the orphan pool, got %d accepted, err %+v", len(accepted), err)
		}
		orphanID := consensushashing.TransactionID(orphan)
		waitingOrphan, ok := mp.orphansPool.allOrphans[*orphanID]
		if !ok {
			t.Fatalf("test setup: orphan is expected to be in the orphan pool")
		}
		if !waitingOrphan.IsHighPriority() {
			t.Fatalf("test setup: a compound orphan is expected to be raised to high priority while it waits")
		}

		if accepted, err := mp.ValidateAndInsertTransaction(parent, false, true, false); err != nil || len(accepted) != 2 {
			t.Fatalf("expected parent and the now-unorphaned transaction to be accepted, got %d accepted, err %+v",
				len(accepted), err)
		}

		promoted, ok := mp.transactionsPool.allTransactions[*orphanID]
		if !ok {
			t.Fatalf("expected the orphan to be promoted into the mempool once its parent arrived")
		}
		if !promoted.IsHighPriority() {
			t.Fatalf("promoted transaction lost the high priority it earned as an orphan - " +
				"it can now expire or be evicted right after becoming eligible to be mined")
		}
	})
}
