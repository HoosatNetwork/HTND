package consensusstatemanager

import (
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/subnetworks"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/utxo"
)

type recordingPrewarmValidator struct {
	model.TransactionValidator
	calls     int
	daaScore  uint64
	prewarmed []*externalapi.DomainTransaction
}

func (v *recordingPrewarmValidator) PrewarmScriptCaches(transactions []*externalapi.DomainTransaction, povDAAScore uint64) {
	v.calls++
	v.daaScore = povDAAScore
	v.prewarmed = append(v.prewarmed, transactions...)
}

func prewarmTestSpend(spends *externalapi.DomainOutpoint, value uint64) *externalapi.DomainTransaction {
	script := &externalapi.ScriptPublicKey{Script: []byte{0x51}, Version: 0}
	return &externalapi.DomainTransaction{
		Inputs:  []*externalapi.DomainTransactionInput{{PreviousOutpoint: *spends}},
		Outputs: []*externalapi.DomainTransactionOutput{{Value: value, ScriptPublicKey: script}},
	}
}

// TestPrewarmMergeSetScriptCachesLeavesTheMergeSetUntouched pins what the prepass may and may not
// do. It hands the validator clones of the transactions whose inputs the selected parent's past
// view holds - once each, however many merge-set blocks carry them - skips the coinbase and every
// transaction the view cannot fully resolve, and never writes a UTXO entry into the merge set's own
// transactions, which the sequential pass would then take as already resolved.
func TestPrewarmMergeSetScriptCachesLeavesTheMergeSetUntouched(t *testing.T) {
	coinA, coinD, coinMissing := outpoint(1, 0), outpoint(4, 0), outpoint(9, 0)
	script := &externalapi.ScriptPublicKey{Script: []byte{0x51}, Version: 0}

	spendA := prewarmTestSpend(coinA, 90)
	spendAAgain := spendA.Clone()
	spendOutputOfA := prewarmTestSpend(externalapi.NewDomainOutpoint(consensushashing.TransactionID(spendA), 0), 80)
	spendMissing := prewarmTestSpend(coinMissing, 70)
	spendD := prewarmTestSpend(coinD, 60)
	coinbase := &externalapi.DomainTransaction{
		SubnetworkID: subnetworks.SubnetworkIDCoinbase,
		Outputs:      []*externalapi.DomainTransactionOutput{{Value: 1, ScriptPublicKey: script}},
	}
	mergeSet := []*externalapi.DomainBlock{
		{Transactions: []*externalapi.DomainTransaction{coinbase, spendA, spendOutputOfA, spendMissing}},
		{Transactions: []*externalapi.DomainTransaction{coinbase.Clone(), spendAAgain, spendD}},
	}

	validator := &recordingPrewarmValidator{}
	csm := &consensusStateManager{
		consensusStateStore: virtualCoins{coins: map[externalapi.DomainOutpoint]externalapi.UTXOEntry{
			*coinA: utxo.NewUTXOEntry(100, script, false, 1),
			*coinD: utxo.NewUTXOEntry(100, script, false, 1),
		}},
		transactionValidator: validator,
	}

	err := csm.prewarmMergeSetScriptCaches(model.NewStagingArea(), mergeSet, utxo.NewMutableUTXODiff().ToImmutable(), 1234)
	if err != nil {
		t.Fatalf("prewarmMergeSetScriptCaches: %+v", err)
	}

	if validator.calls != 1 || validator.daaScore != 1234 {
		t.Fatalf("PrewarmScriptCaches called %d times with DAA score %d, want once with 1234",
			validator.calls, validator.daaScore)
	}
	wantIDs := map[externalapi.DomainTransactionID]bool{
		*consensushashing.TransactionID(spendA): true,
		*consensushashing.TransactionID(spendD): true,
	}
	if len(validator.prewarmed) != len(wantIDs) {
		t.Fatalf("prewarmed %d transactions, want %d (the spends of A and D)", len(validator.prewarmed), len(wantIDs))
	}
	originals := map[*externalapi.DomainTransaction]bool{spendA: true, spendAAgain: true, spendD: true}
	for _, prewarmed := range validator.prewarmed {
		if !wantIDs[*consensushashing.TransactionID(prewarmed)] {
			t.Fatalf("prewarmed unexpected transaction %s", consensushashing.TransactionID(prewarmed))
		}
		if originals[prewarmed] {
			t.Fatalf("prewarmed a merge-set transaction itself instead of a clone")
		}
		for i, input := range prewarmed.Inputs {
			if input.UTXOEntry == nil {
				t.Fatalf("prewarmed transaction %s input %d has no UTXO entry", consensushashing.TransactionID(prewarmed), i)
			}
		}
	}

	for _, block := range mergeSet {
		for _, transaction := range block.Transactions {
			for i, input := range transaction.Inputs {
				if input.UTXOEntry != nil {
					t.Fatalf("merge-set transaction %s input %d was given a UTXO entry by the prepass",
						consensushashing.TransactionID(transaction), i)
				}
			}
		}
	}
}

// TestPrewarmMergeSetScriptCachesSkipsASingleTransaction pins that one candidate is left to the
// sequential pass: there is nothing to run in parallel with it.
func TestPrewarmMergeSetScriptCachesSkipsASingleTransaction(t *testing.T) {
	coin := outpoint(1, 0)
	script := &externalapi.ScriptPublicKey{Script: []byte{0x51}, Version: 0}
	validator := &recordingPrewarmValidator{}
	csm := &consensusStateManager{
		consensusStateStore: virtualCoins{coins: map[externalapi.DomainOutpoint]externalapi.UTXOEntry{
			*coin: utxo.NewUTXOEntry(100, script, false, 1),
		}},
		transactionValidator: validator,
	}
	mergeSet := []*externalapi.DomainBlock{{Transactions: []*externalapi.DomainTransaction{prewarmTestSpend(coin, 90)}}}

	err := csm.prewarmMergeSetScriptCaches(model.NewStagingArea(), mergeSet, utxo.NewMutableUTXODiff().ToImmutable(), 1)
	if err != nil {
		t.Fatalf("prewarmMergeSetScriptCaches: %+v", err)
	}
	if validator.calls != 0 {
		t.Fatalf("PrewarmScriptCaches called %d times for a single candidate, want 0", validator.calls)
	}
}
