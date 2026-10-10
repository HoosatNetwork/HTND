package mempool

import (
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/testutils"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/txscript"
	"github.com/HoosatNetwork/HTND/v2/domain/consensusreference"
	"github.com/HoosatNetwork/HTND/v2/domain/miningmanager/mempool/model"
)

// TestOrphanPoolEvictsRelayedCompoundOrphans pins that relayed orphans stay bounded by
// MaximumOrphanTransactionCount even when they are raised to high priority for looking like compound
// transactions. The orphan pool used to exempt every high-priority orphan from eviction, so relayed compound
// orphans whose parents never arrived accumulated past the limit without end ("Number of high-priority
// transactions in orphanPool (1207) is higher than maximum allowed (100)"). Orphans this node's own RPC
// submitted as high priority keep their exemption.
func TestOrphanPoolEvictsRelayedCompoundOrphans(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		tc, teardown, err := consensus.NewFactory().NewTestConsensus(consensusConfig, "TestOrphanPoolEvictsRelayedCompoundOrphans")
		if err != nil {
			t.Fatalf("Error setting up consensus: %+v", err)
		}
		defer teardown(false)

		tcAsConsensus := tc.(externalapi.Consensus)
		tcAsConsensusPointer := &tcAsConsensus
		config := DefaultConfig(tc.DAGParams())
		config.MaximumOrphanTransactionCount = 2
		config.CompoundTxMinInputsThreshold = 2
		mp := New(config, consensusreference.NewConsensusReference(&tcAsConsensusPointer)).(*mempool)

		scriptPublicKey, redeemScript := testutils.OpTrueScript()
		signatureScript, err := txscript.PayToScriptHashSignatureScript(redeemScript, nil)
		if err != nil {
			t.Fatalf("PayToScriptHashSignatureScript: %+v", err)
		}
		// newOrphan spends two outputs of transactions this node has never seen, so it has the compound
		// shape (inputs >= CompoundTxMinInputsThreshold) and waits in the orphan pool.
		newOrphan := func(seed byte) *externalapi.DomainTransaction {
			inputs := make([]*externalapi.DomainTransactionInput, 2)
			for i := range inputs {
				inputs[i] = &externalapi.DomainTransactionInput{
					PreviousOutpoint: externalapi.DomainOutpoint{
						TransactionID: *externalapi.NewDomainTransactionIDFromByteArray(&[externalapi.DomainHashSize]byte{seed, byte(i)}),
					},
					SignatureScript: signatureScript,
					Sequence:        constants.MaxTxInSequenceNum,
				}
			}
			return &externalapi.DomainTransaction{
				Version: constants.MaxTransactionVersion,
				Inputs:  inputs,
				Outputs: []*externalapi.DomainTransactionOutput{{ScriptPublicKey: scriptPublicKey, Value: 1_000_000}},
				Payload: []byte{},
			}
		}

		const orphanCount = 5
		for i := range orphanCount {
			if _, err := mp.ValidateAndInsertTransaction(newOrphan(byte(i+1)), false, true, false); err != nil {
				t.Fatalf("ValidateAndInsertTransaction(relayed orphan): %+v", err)
			}
		}
		if got := uint64(len(mp.orphansPool.allOrphans)); got > config.MaximumOrphanTransactionCount {
			t.Fatalf("relayed compound orphans must be evicted down to the limit of %d, the pool holds %d",
				config.MaximumOrphanTransactionCount, got)
		}
		for _, orphan := range mp.orphansPool.allOrphans {
			if !orphan.IsHighPriority() {
				t.Fatalf("test setup: relayed compound orphans are expected to be raised to high priority")
			}
		}

		for i := range orphanCount {
			if _, err := mp.ValidateAndInsertTransaction(newOrphan(byte(100+i)), true, true, true); err != nil {
				t.Fatalf("ValidateAndInsertTransaction(local orphan): %+v", err)
			}
		}
		localOrphans := 0
		for _, orphan := range mp.orphansPool.allOrphans {
			if orphan.IsLocalSubmission() {
				localOrphans++
			}
		}
		if localOrphans != orphanCount {
			t.Fatalf("locally submitted high-priority orphans must not be evicted: %d of %d remain", localOrphans, orphanCount)
		}
	})
}

// TestIsProtectedOrphan pins the exemption rule shared by eviction and expiry.
func TestIsProtectedOrphan(t *testing.T) {
	transaction := &externalapi.DomainTransaction{}
	tests := []struct {
		isHighPriority, isLocalSubmission, protected bool
	}{
		{isHighPriority: true, isLocalSubmission: true, protected: true},
		{isHighPriority: true, isLocalSubmission: false, protected: false},
		{isHighPriority: false, isLocalSubmission: true, protected: false},
		{isHighPriority: false, isLocalSubmission: false, protected: false},
	}
	for _, test := range tests {
		orphan := model.NewOrphanTransaction(transaction, test.isHighPriority, test.isLocalSubmission, 0)
		if got := isProtectedOrphan(orphan); got != test.protected {
			t.Errorf("highPriority=%t local=%t: protected=%t, want %t",
				test.isHighPriority, test.isLocalSubmission, got, test.protected)
		}
	}
}
