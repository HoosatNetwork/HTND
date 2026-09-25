package mempool

import (
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/testutils"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/txscript"
	"github.com/HoosatNetwork/HTND/v2/domain/consensusreference"
)

// TestSpendingOutOfRangeOutputOfPoolTransaction pins that a transaction spending an output index a mempool
// transaction does not have is rejected rather than crashing the node. Inputs spending pool transactions were
// filled by indexing the parent's outputs with the peer-supplied index before anything checked it, so one relayed
// transaction naming any public mempool transaction ID with a too-large index panicked the node.
func TestSpendingOutOfRangeOutputOfPoolTransaction(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		tc, teardown, err := consensus.NewFactory().NewTestConsensus(consensusConfig, "TestSpendingOutOfRangeOutputOfPoolTransaction")
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
		parent, err := testutils.CreateTransaction(funding, 1_000)
		if err != nil {
			t.Fatalf("CreateTransaction(parent): %+v", err)
		}
		if accepted, err := mp.ValidateAndInsertTransaction(parent, false, true, false); err != nil || len(accepted) != 1 {
			t.Fatalf("expected the parent to be accepted, got %d accepted, err %+v", len(accepted), err)
		}

		scriptPublicKey, redeemScript := testutils.OpTrueScript()
		signatureScript, err := txscript.PayToScriptHashSignatureScript(redeemScript, nil)
		if err != nil {
			t.Fatalf("PayToScriptHashSignatureScript: %+v", err)
		}
		parentID := consensushashing.TransactionID(parent)
		for _, index := range []uint32{uint32(len(parent.Outputs)), ^uint32(0)} {
			for _, allowOrphan := range []bool{true, false} {
				child := &externalapi.DomainTransaction{
					Version: constants.MaxTransactionVersion,
					Inputs: []*externalapi.DomainTransactionInput{{
						PreviousOutpoint: externalapi.DomainOutpoint{TransactionID: *parentID, Index: index},
						SignatureScript:  signatureScript,
						Sequence:         constants.MaxTxInSequenceNum,
					}},
					Outputs: []*externalapi.DomainTransactionOutput{{
						ScriptPublicKey: scriptPublicKey,
						Value:           parent.Outputs[0].Value - 1_000,
					}},
					Payload: []byte{},
				}
				childID := consensushashing.TransactionID(child)

				accepted, err := mp.ValidateAndInsertTransaction(child, false, allowOrphan, false)
				if len(accepted) != 0 {
					t.Fatalf("index %d, allowOrphan %t: a transaction spending a non-existent output was accepted", index, allowOrphan)
				}
				if !allowOrphan && err == nil {
					t.Fatalf("index %d: expected a rule error when orphans are not allowed", index)
				}
				if _, _, found := mp.GetTransaction(childID, true, false); found {
					t.Fatalf("index %d, allowOrphan %t: the transaction is in the transaction pool", index, allowOrphan)
				}
			}
		}
	})
}
