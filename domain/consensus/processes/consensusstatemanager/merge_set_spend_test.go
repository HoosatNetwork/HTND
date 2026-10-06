package consensusstatemanager

import (
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/ruleerrors"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/utxo"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/utxosurvey"
	"github.com/pkg/errors"
)

// virtualCoins is the materialized UTXO table a node holds at virtual. The diff passed beside it is
// the separate question of what the block being validated can see.
type virtualCoins struct {
	model.ConsensusStateStore
	coins map[externalapi.DomainOutpoint]externalapi.UTXOEntry
}

func (s virtualCoins) LookupUTXOByOutpoint(_ model.DBReader, _ *model.StagingArea, outpoint *externalapi.DomainOutpoint) (
	externalapi.UTXOEntry, bool, error,
) {
	if outpoint == nil {
		return nil, false, nil
	}
	entry, ok := s.coins[*outpoint]
	return entry, ok, nil
}

type acceptingTransactionValidator struct{ model.TransactionValidator }

func (acceptingTransactionValidator) ValidateTransactionInContextAndPopulateFee(
	*model.StagingArea, *externalapi.DomainTransaction, *externalapi.DomainHash, uint64,
) error {
	return nil
}

// TestVirtualPositionDoesNotChangeMissingInputVerdict is the arrival-order split. Two nodes hold the
// same past, in which coin A is spendable and coin C is not. One node's virtual still holds C, so
// the virtual-to-past diff lists C in ToRemove. The other node's virtual has never held C, so the
// diff does not mention it. Both must call C missing, leave it unresolved, and accept the transaction
// when the offset gate is on.
func TestVirtualPositionDoesNotChangeMissingInputVerdict(t *testing.T) {
	coinA := outpoint(1, 0)
	coinC := outpoint(2, 0)
	script := &externalapi.ScriptPublicKey{Script: []byte{0x51}, Version: 0}
	entryA := utxo.NewUTXOEntry(100, script, false, 5)
	entryC := utxo.NewUTXOEntry(50, script, false, 9)

	holdsC, err := utxo.NewUTXODiffFromCollections(
		utxo.NewUTXOCollection(nil),
		utxo.NewUTXOCollection(map[externalapi.DomainOutpoint]externalapi.UTXOEntry{*coinC: entryC}))
	if err != nil {
		t.Fatalf("diff: %v", err)
	}

	verdict := func(virtual map[externalapi.DomainOutpoint]externalapi.UTXOEntry, diff externalapi.UTXODiff) bool {
		t.Helper()
		csm := &consensusStateManager{consensusStateStore: virtualCoins{coins: virtual}}
		tx := &externalapi.DomainTransaction{Inputs: []*externalapi.DomainTransactionInput{
			{PreviousOutpoint: *coinA},
			{PreviousOutpoint: *coinC},
		}}
		err := csm.populateTransactionWithUTXOEntriesFromVirtualOrDiff(nil, tx, diff, nil)
		var missing ruleerrors.ErrMissingTxOut
		if !errors.As(err, &missing) {
			t.Fatalf("expected a missing-outpoint error, got %v", err)
		}
		if missing.HasDoubleSpend() {
			t.Fatalf("coin C is not in this block's past, so it is missing, not a double spend: %+v", missing.SpentOutpoints)
		}
		if tx.Inputs[1].UTXOEntry != nil {
			t.Fatal("the absent coin was filled from virtual")
		}
		if tx.Inputs[0].UTXOEntry == nil {
			t.Fatal("the coin the past does hold was not filled")
		}
		resolved := 0
		for _, input := range tx.Inputs {
			if input.UTXOEntry != nil {
				resolved++
			}
		}
		return acceptDespiteMissingInputs(err, true, resolved)
	}

	virtualHoldsC := verdict(
		map[externalapi.DomainOutpoint]externalapi.UTXOEntry{*coinA: entryA, *coinC: entryC}, holdsC)
	virtualLacksC := verdict(
		map[externalapi.DomainOutpoint]externalapi.UTXOEntry{*coinA: entryA}, utxo.NewUTXODiff())
	if !virtualHoldsC || !virtualLacksC {
		t.Fatalf("offset-chain acceptance of the same transaction: virtual holds C=%t, virtual lacks C=%t",
			virtualHoldsC, virtualLacksC)
	}
}

// TestSameMergeSetPassSpendIsADoubleSpend locks the spend set the acceptance loop actually passes
// through. The first transaction spends a coin virtual holds. The second spends it again in the same
// pass and must be rejected as a double spend, including when the first spend only cancelled a ToAdd
// entry and left nothing in ToRemove.
func TestSameMergeSetPassSpendIsADoubleSpend(t *testing.T) {
	t.Setenv("HTND_UTXO_SURVEY", "/dev/null")
	utxosurvey.Reset()
	t.Cleanup(utxosurvey.Reset)

	coin := outpoint(7, 0)
	script := &externalapi.ScriptPublicKey{Script: []byte{0x51}, Version: 0}
	entry := utxo.NewUTXOEntry(40, script, false, 3)
	blockHash := externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{7})
	spentInPass := make(map[externalapi.DomainOutpoint]struct{})
	csm := &consensusStateManager{
		consensusStateStore:  virtualCoins{coins: map[externalapi.DomainOutpoint]externalapi.UTXOEntry{*coin: entry}},
		transactionValidator: acceptingTransactionValidator{},
		pruningStore:         noPruningPointStore{},
		ghostdagDataStore:    missingGHOSTDAGDataStore{},
	}
	diff := utxo.NewMutableUTXODiff()

	spend := func() (bool, *transactionRejection) {
		t.Helper()
		tx := &externalapi.DomainTransaction{Inputs: []*externalapi.DomainTransactionInput{{PreviousOutpoint: *coin}}}
		accepted, _, rejection, err := csm.maybeAcceptTransaction(
			model.NewStagingArea(), tx, blockHash, blockHash, false, diff, spentInPass, 0, 0, 1)
		if err != nil {
			t.Fatalf("maybeAcceptTransaction: %+v", err)
		}
		return accepted, rejection
	}

	accepted, rejection := spend()
	if !accepted {
		t.Fatalf("the first spend was rejected: %+v", rejection)
	}
	if _, ok := spentInPass[*coin]; !ok {
		t.Fatal("accepting the first spend did not record the outpoint")
	}

	accepted, rejection = spend()
	if accepted {
		t.Fatal("a second spend of the same outpoint in one merge-set pass was accepted")
	}
	if rejection == nil || rejection.reason != "double-spend" {
		t.Fatalf("the second spend must be a double spend, got %+v", rejection)
	}

	// A coin that exists only in the past sits in ToAdd. Spending it cancels that entry and does not
	// write ToRemove, so the pass set is the only record of the spend.
	onlyInPast, err := utxo.NewUTXODiffFromCollections(
		utxo.NewUTXOCollection(map[externalapi.DomainOutpoint]externalapi.UTXOEntry{*coin: entry}),
		utxo.NewUTXOCollection(nil))
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	mutable := onlyInPast.CloneMutable()
	first := &externalapi.DomainTransaction{Inputs: []*externalapi.DomainTransactionInput{{PreviousOutpoint: *coin}}}
	if err := csm.populateTransactionWithUTXOEntriesFromVirtualOrDiff(nil, first, mutable.ToImmutable(), nil); err != nil {
		t.Fatalf("populating the past coin: %v", err)
	}
	if err := mutable.AddTransaction(first, 1); err != nil {
		t.Fatalf("spending the past coin: %v", err)
	}
	if mutable.ToRemove().Contains(coin) {
		t.Fatal("spending a ToAdd coin must cancel it, not leave it in ToRemove")
	}
	pass := map[externalapi.DomainOutpoint]struct{}{*coin: {}}
	second := &externalapi.DomainTransaction{Inputs: []*externalapi.DomainTransactionInput{{PreviousOutpoint: *coin}}}
	err = csm.populateTransactionWithUTXOEntriesFromVirtualOrDiff(nil, second, mutable.ToImmutable(), pass)
	var missing ruleerrors.ErrMissingTxOut
	if !errors.As(err, &missing) || !missing.HasDoubleSpend() {
		t.Fatalf("a second spend after a cancelled ToAdd must be a double spend, got %v", err)
	}
}
