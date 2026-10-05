package consensus_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/testapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/testutils"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/txscript"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
)

// TestPastUTXOReconstructionDoesNotDependOnReorgHistory is the HTN-004 repro attempt: nodes holding the same blocks,
// received in orders that switch the selected tip a different number of times, must rebuild the same past UTXO set
// for every block they can rebuild. The same transaction is merged at different heights on two branches (so its
// output carries a different DAA stamp on each) and its output is spent on both, which is the shape the diff-algebra
// tolerances were written for. HTND_REORG_REPRO_ATTEMPTS scales the number of attempts (block hashes vary).
func TestPastUTXOReconstructionDoesNotDependOnReorgHistory(t *testing.T) {
	previous := constants.GetBlockVersion()
	t.Cleanup(func() { constants.ForceSetBlockVersion(uint(previous)) })

	attempts := envInt("HTND_REORG_REPRO_ATTEMPTS", 6)
	var failures []string
	for attempt := 0; attempt < attempts; attempt++ {
		failures = append(failures, reorgReconstructionScenario(t, attempt)...)
	}
	if len(failures) > 0 {
		t.Fatalf("%d reconstruction difference(s):\n%s", len(failures), strings.Join(failures, "\n"))
	}
}

func reorgReconstructionScenario(t *testing.T, attempt int) []string {
	params := dagconfig.MainnetParams
	params.POWScores = []uint64{1, 1, 1, 1, 1}
	params.BlockCoinbaseMaturity = 0
	factory := consensus.NewFactory()
	newNode := func(name string) testapi.TestConsensus {
		config := &consensus.Config{Params: params}
		config.SkipProofOfWork = true
		tc, teardown, err := factory.NewTestConsensus(config, fmt.Sprintf("ReorgRepro%s_%d", name, attempt))
		if err != nil {
			t.Fatalf("NewTestConsensus: %+v", err)
		}
		t.Cleanup(func() { teardown(false) })
		return tc
	}
	minerP, minerR := newNode("MinerP"), newNode("MinerR")
	type observer struct {
		name string
		tc   testapi.TestConsensus
	}
	observers := []observer{{"flip-flop", newNode("FlipFlop")}, {"p-first", newNode("PFirst")}, {"r-first", newNode("RFirst")}}
	constants.ForceSetBlockVersion(6)

	scriptPublicKey, redeemScript := testutils.OpTrueScript()
	signatureScript, err := txscript.PayToScriptHashSignatureScript(redeemScript, nil)
	if err != nil {
		t.Fatalf("signature script: %+v", err)
	}
	spend := func(outpoint externalapi.DomainOutpoint, value, fee uint64) *externalapi.DomainTransaction {
		return &externalapi.DomainTransaction{
			Version: constants.MaxTransactionVersion,
			Inputs: []*externalapi.DomainTransactionInput{{
				PreviousOutpoint: outpoint, SignatureScript: signatureScript, Sequence: constants.MaxTxInSequenceNum,
			}},
			Outputs: []*externalapi.DomainTransactionOutput{{Value: value - fee, ScriptPublicKey: scriptPublicKey}},
			Payload: []byte{},
		}
	}
	tags := map[externalapi.DomainHash]string{}
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
		block, err := tc.BuildBlock(&externalapi.DomainCoinbaseData{
			ScriptPublicKey: scriptPublicKey, ExtraData: []byte(fmt.Sprintf("%s/%d", tag, attempt)),
		}, populated)
		if err != nil {
			t.Fatalf("%s: BuildBlock: %+v", tag, err)
		}
		if err := tc.ValidateAndInsertBlock(relayed(block), true, true); err != nil {
			t.Fatalf("%s: the miner's own node rejected it: %+v", tag, err)
		}
		tags[*consensushashing.BlockHash(block)] = tag
		return block
	}
	deliver := func(tc testapi.TestConsensus, blocks ...*externalapi.DomainBlock) {
		t.Helper()
		for _, block := range blocks {
			if err := tc.ValidateAndInsertBlock(relayed(block), true, true); err != nil {
				t.Fatalf("delivering %s: %+v", tags[*consensushashing.BlockHash(block)], err)
			}
		}
	}

	var prefix []*externalapi.DomainBlock
	for i := 0; i < 4; i++ {
		prefix = append(prefix, mine(minerP, fmt.Sprintf("prefix%d", i)))
	}
	deliver(minerR, prefix...)
	for _, o := range observers {
		deliver(o.tc, prefix...)
	}

	funding := prefix[1].Transactions[0]
	transactionT := spend(externalapi.DomainOutpoint{TransactionID: *consensushashing.TransactionID(funding), Index: 0},
		funding.Outputs[0].Value, 1_000)
	outputOfT := externalapi.DomainOutpoint{TransactionID: *consensushashing.TransactionID(transactionT), Index: 0}
	valueOfT := transactionT.Outputs[0].Value

	// Stages alternate between the branches, each making its branch the longer one.
	var stages [][]*externalapi.DomainBlock
	stages = append(stages, []*externalapi.DomainBlock{mine(minerP, "P1", transactionT), mine(minerP, "P2", spend(outputOfT, valueOfT, 1_000))})
	stages = append(stages, []*externalapi.DomainBlock{mine(minerR, "R1"), mine(minerR, "R2", transactionT), mine(minerR, "R3")})
	stages = append(stages, []*externalapi.DomainBlock{mine(minerP, "P3"), mine(minerP, "P4")})
	stages = append(stages, []*externalapi.DomainBlock{mine(minerR, "R4", spend(outputOfT, valueOfT, 2_000)), mine(minerR, "R5")})
	stages = append(stages, []*externalapi.DomainBlock{mine(minerP, "P5"), mine(minerP, "P6")})
	stages = append(stages, []*externalapi.DomainBlock{mine(minerR, "R6"), mine(minerR, "R7")})

	var pBlocks, rBlocks []*externalapi.DomainBlock
	for i, stage := range stages {
		deliver(observers[0].tc, stage...)
		if i%2 == 0 {
			pBlocks = append(pBlocks, stage...)
		} else {
			rBlocks = append(rBlocks, stage...)
		}
	}
	deliver(observers[1].tc, pBlocks...)
	deliver(observers[1].tc, rBlocks...)
	deliver(observers[2].tc, rBlocks...)
	deliver(observers[2].tc, pBlocks...)

	var failures []string
	report := func(format string, args ...any) {
		failures = append(failures, fmt.Sprintf("attempt %d: ", attempt)+fmt.Sprintf(format, args...))
	}

	tips := make([]*externalapi.DomainHash, len(observers))
	for i, o := range observers {
		tip, err := o.tc.GetVirtualSelectedParent()
		if err != nil {
			t.Fatalf("GetVirtualSelectedParent: %+v", err)
		}
		tips[i] = tip
	}
	for i := range observers[1:] {
		if !tips[i+1].Equal(tips[0]) {
			report("final tip differs: %s has %s, %s has %s", observers[0].name, tags[*tips[0]], observers[i+1].name, tags[*tips[i+1]])
		}
	}

	allBlocks := append(append(append([]*externalapi.DomainBlock(nil), prefix...), pBlocks...), rBlocks...)
	restored := 0
	for _, block := range allBlocks {
		hash := consensushashing.BlockHash(block)
		tag := tags[*hash]
		statuses := make([]externalapi.BlockStatus, len(observers))
		multisets := make([]*externalapi.DomainHash, len(observers))
		for i, o := range observers {
			info, err := o.tc.GetBlockInfo(hash)
			if err != nil || !info.Exists {
				t.Fatalf("GetBlockInfo %s on %s: %+v", tag, o.name, err)
			}
			statuses[i] = info.BlockStatus
			if restoredHash, err := restoredPastMultisetHash(o.tc, hash); err == nil {
				multisets[i] = restoredHash
			}
		}
		// A block on a branch a node never had on its selected chain is left pending verification there, while a
		// node that did resolve it holds a verdict; that is not a disagreement. Any two verdicts must match.
		resolved := func(status externalapi.BlockStatus) bool {
			return status != externalapi.StatusUTXOPendingVerification
		}
		for i := 1; i < len(observers); i++ {
			if statuses[i] != statuses[0] && resolved(statuses[i]) && resolved(statuses[0]) {
				report("%s is %s on %s but %s on %s", tag, statuses[0], observers[0].name, statuses[i], observers[i].name)
			}
		}
		if statuses[1] != statuses[2] && resolved(statuses[1]) && resolved(statuses[2]) {
			report("%s is %s on %s but %s on %s", tag, statuses[1], observers[1].name, statuses[2], observers[2].name)
		}
		header, err := observers[0].tc.BlockHeaderStore().BlockHeader(observers[0].tc.DatabaseContext(), model.NewStagingArea(), hash)
		if err != nil {
			t.Fatalf("header of %s: %+v", tag, err)
		}
		for i, o := range observers {
			if multisets[i] == nil {
				continue
			}
			restored++
			if statuses[i] == externalapi.StatusUTXOValid && !multisets[i].Equal(header.UTXOCommitment()) {
				report("%s restores %s's past to %s, but its header commits to %s", o.name, tag, multisets[i], header.UTXOCommitment())
			}
			for j := i + 1; j < len(observers); j++ {
				if multisets[j] != nil && !multisets[j].Equal(multisets[i]) {
					report("%s restores %s's past to %s, %s to %s", o.name, tag, multisets[i], observers[j].name, multisets[j])
				}
			}
		}
	}
	t.Logf("attempt %d: final tip %s on all observers, %d past reconstructions compared, %d difference(s)",
		attempt, tags[*tips[0]], restored, len(failures))
	return failures
}
