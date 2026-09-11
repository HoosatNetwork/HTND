package consensus_test

import (
	"fmt"
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus"
	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/testapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/testutils"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/txscript"
	"github.com/HoosatNetwork/HTND/domain/dagconfig"
)

// TestReceiverAcceptsBlocksBuiltOnATipItReorganisedAway is the relayed-block disqualification,
// reduced to the smallest DAG that produces it.
//
// A coin's DAA stamp is the DAA score of the block that merged the transaction creating it, so the
// same coin carries a different stamp on two branches that merged that transaction at different
// heights. When a sibling branch takes the tip, the old tip's stored UTXO diff is rewritten relative
// to the new tip, and it has to record that difference. reconcileWinningBranchUTXO erased it, so every
// later reconstruction of the old tip's past carried the new branch's stamp. Nothing noticed until the
// old branch won again and a block built on it spent the coin - at which point the receiver computed a
// different UTXO commitment from the one the block's miner computed, disqualified the block, and then
// disqualified everything built on it.
//
// The receiver mines P1 (with transaction T) and P2 (spending T's output), the rival mines R1, R2
// (with the same T) and R3, and a sibling holding P1..P2 extends them with P3 and P4. The receiver
// takes R1..R3, switching its tip to the rival's branch, and then P3..P4, which win back.
func TestReceiverAcceptsBlocksBuiltOnATipItReorganisedAway(t *testing.T) {
	previous := constants.GetBlockVersion()
	t.Cleanup(func() { constants.ForceSetBlockVersion(uint(previous)) })

	for attempt := 0; attempt < 20; attempt++ {
		if siblingTipSwitchScenario(t, attempt) {
			return
		}
	}
	t.Fatalf("in 20 attempts R2 always won its length tie against P2, so the switch this test is about never happened")
}

// siblingTipSwitchScenario runs the scenario once and reports whether it exercised the switch. R2 ties
// P2 on length, and the hash decides the tie: if R2 takes the tip on its own, T's output never holds
// two stamps at a switch, and the attempt says nothing about the bug.
func siblingTipSwitchScenario(t *testing.T, attempt int) bool {
	params := dagconfig.MainnetParams
	params.POWScores = []uint64{1, 1, 1, 1, 1}
	params.BlockCoinbaseMaturity = 0
	factory := consensus.NewFactory()
	newNode := func(name string) testapi.TestConsensus {
		config := &consensus.Config{Params: params}
		config.SkipProofOfWork = true
		tc, teardown, err := factory.NewTestConsensus(config, fmt.Sprintf("%s_%d", name, attempt))
		if err != nil {
			t.Fatalf("NewTestConsensus: %+v", err)
		}
		t.Cleanup(func() { teardown(false) })
		return tc
	}
	receiver, rival, sibling := newNode("SiblingSwitchReceiver"), newNode("SiblingSwitchRival"), newNode("SiblingSwitchSibling")
	// NewTestConsensus resets the process-wide block version; pin it after the last node exists.
	constants.ForceSetBlockVersion(6)

	scriptPublicKey, redeemScript := testutils.OpTrueScript()
	signatureScript, err := txscript.PayToScriptHashSignatureScript(redeemScript, nil)
	if err != nil {
		t.Fatalf("signature script: %+v", err)
	}
	spend := func(outpoint externalapi.DomainOutpoint, value uint64) *externalapi.DomainTransaction {
		return &externalapi.DomainTransaction{
			Version: constants.MaxTransactionVersion,
			Inputs: []*externalapi.DomainTransactionInput{{
				PreviousOutpoint: outpoint, SignatureScript: signatureScript, Sequence: constants.MaxTxInSequenceNum,
			}},
			Outputs: []*externalapi.DomainTransactionOutput{{Value: value - 1_000, ScriptPublicKey: scriptPublicKey}},
			Payload: []byte{},
		}
	}
	mine := func(tc testapi.TestConsensus, tag string, transactions ...*externalapi.DomainTransaction) *externalapi.DomainBlock {
		t.Helper()
		populated := make([]*externalapi.DomainTransaction, 0, len(transactions))
		for _, transaction := range transactions {
			clone := transaction.Clone()
			if err := tc.ValidateTransactionAndPopulateWithConsensusData(clone); err != nil {
				t.Fatalf("%s: transaction does not validate on the miner's own virtual: %+v", tag, err)
			}
			populated = append(populated, clone)
		}
		block, err := tc.BuildBlock(&externalapi.DomainCoinbaseData{ScriptPublicKey: scriptPublicKey, ExtraData: []byte(tag)}, populated)
		if err != nil {
			t.Fatalf("%s: BuildBlock: %+v", tag, err)
		}
		if err := tc.ValidateAndInsertBlock(relayed(block), true, true); err != nil {
			t.Fatalf("%s: the miner's own node rejected it: %+v", tag, err)
		}
		return block
	}
	deliver := func(tc testapi.TestConsensus, blocks ...*externalapi.DomainBlock) {
		t.Helper()
		for _, block := range blocks {
			if err := tc.ValidateAndInsertBlock(relayed(block), true, true); err != nil {
				t.Fatalf("delivering %s: %+v", consensushashing.BlockHash(block), err)
			}
		}
	}
	status := func(tc testapi.TestConsensus, block *externalapi.DomainBlock) externalapi.BlockStatus {
		info, err := tc.GetBlockInfo(consensushashing.BlockHash(block))
		if err != nil || !info.Exists {
			t.Fatalf("GetBlockInfo: %+v", err)
		}
		return info.BlockStatus
	}
	tipOf := func(tc testapi.TestConsensus) *externalapi.DomainHash {
		tip, err := tc.GetVirtualSelectedParent()
		if err != nil {
			t.Fatalf("GetVirtualSelectedParent: %+v", err)
		}
		return tip
	}

	// A shared prefix, so the fork happens above a common chain as it would on a live network.
	var prefix []*externalapi.DomainBlock
	for i := 0; i < 4; i++ {
		prefix = append(prefix, mine(receiver, "prefix"))
	}
	deliver(rival, prefix...)
	deliver(sibling, prefix...)

	// T spends a prefix coinbase output that is in every node's virtual.
	funding := prefix[1].Transactions[0]
	transactionT := spend(externalapi.DomainOutpoint{TransactionID: *consensushashing.TransactionID(funding), Index: 0},
		funding.Outputs[0].Value)
	outputOfT := externalapi.DomainOutpoint{TransactionID: *consensushashing.TransactionID(transactionT), Index: 0}

	// The receiver's branch: P1 carries T, and P2 spends T's output, so T's output is in P2's past but
	// not in the virtual on top of P2.
	p1 := mine(receiver, "P1", transactionT)
	p2 := mine(receiver, "P2", spend(outputOfT, transactionT.Outputs[0].Value))
	deliver(sibling, p1, p2)

	// The rival's branch merges T one block later, so T's output carries a different stamp there.
	r1 := mine(rival, "R1")
	r2 := mine(rival, "R2", transactionT)
	r3 := mine(rival, "R3")

	p2Hash := consensushashing.BlockHash(p2)
	deliver(receiver, r1, r2)
	if !tipOf(receiver).Equal(p2Hash) {
		return false
	}
	deliver(receiver, r3)
	if !tipOf(receiver).Equal(consensushashing.BlockHash(r3)) {
		t.Fatalf("R3 did not take the receiver's tip from P2")
	}

	// The old tip's past, rebuilt from the diffs stored at the switch, must still be the past its own
	// header commits to. This is the assertion that fails at the moment of corruption.
	header, err := receiver.BlockHeaderStore().BlockHeader(receiver.DatabaseContext(), model.NewStagingArea(), p2Hash)
	if err != nil {
		t.Fatalf("header of P2: %+v", err)
	}
	if restored, err := restoredPastMultisetHash(receiver, p2Hash); err != nil {
		t.Fatalf("restoring P2's past: %+v", err)
	} else if !restored.Equal(header.UTXOCommitment()) {
		t.Errorf("after the rival's branch took the tip, the receiver's stored diffs no longer rebuild P2's past: "+
			"got %s, P2's header commits to %s", restored, header.UTXOCommitment())
	}

	// And the symptom: the sibling's extension of P2 wins back, and the receiver has to agree with the
	// node that mined it.
	p3 := mine(sibling, "P3")
	p4 := mine(sibling, "P4")
	deliver(receiver, p3, p4)
	for _, check := range []struct {
		name  string
		block *externalapi.DomainBlock
	}{{"P3", p3}, {"P4", p4}} {
		if onSibling, onReceiver := status(sibling, check.block), status(receiver, check.block); onReceiver != onSibling {
			t.Errorf("%s is %s on the sibling that mined it but %s on the receiver", check.name, onSibling, onReceiver)
		}
	}
	return true
}
