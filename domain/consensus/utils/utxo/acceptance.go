package utxo

import (
	"math"

	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/consensushashing"
	"github.com/pkg/errors"
)

// MultisetWriter is the part of a multiset that applying acceptance data needs. It is declared here,
// rather than taking model.Multiset, only so that this package - which owns UTXO serialization -
// stays free of any dependency on the consensus model package.
type MultisetWriter interface {
	Add(data []byte)
	Remove(data []byte)
}

// AcceptedUTXOBlockDAAScore returns the DAA score that must be stamped into every UTXOEntry created
// by applying a block's acceptance data.
//
// THE RULE, stated once for the whole codebase: a UTXO is stamped with the DAA score of the block
// that ADDED IT TO THE UTXO SET - the merging block, i.e. the block whose past UTXO set is being
// resolved - and NOT with the DAA score of the merge-set block whose body happens to contain the
// transaction. A transaction only enters the UTXO set when a chain block merges and accepts it, so
// the merging block is the block that created the coin as far as the UTXO set is concerned.
//
// This is a consensus rule, not an implementation detail: BlockDAAScore is serialized into the
// multiset preimage by SerializeUTXO, so it is committed to by every block header's
// UTXOCommitment, and it is the value coinbase maturity is measured from. Every block on mainnet
// was mined under this rule (see TestUTXOCommitmentDAAStampRule, which pins it), so stamping a
// merge-set block's own DAA score instead makes this node compute a different commitment than the
// one in the header for essentially every block that exists, i.e. ErrBadUTXOCommitment on the
// entire chain.
//
// The rule is unambiguous and consistent by construction: along any given selected chain each
// merge-set block is merged by exactly one chain block, so each coin is stamped exactly once, and
// resolving any later block's past UTXO never re-stamps it - later blocks inherit it through the
// accumulated UTXO diffs.
//
// It exists as a named function taking the merging block's score so that every site that replays
// acceptance data names the rule instead of re-deriving it.
func AcceptedUTXOBlockDAAScore(mergingBlockDAAScore uint64) uint64 {
	return mergingBlockDAAScore
}

// ApplyAcceptanceDataToMultiset applies every accepted transaction in acceptanceData to ms - removing
// each spent input's UTXO and adding each created output's UTXO - stamping created entries per
// AcceptedUTXOBlockDAAScore. mergingBlockDAAScore is the DAA score of the block that acceptanceData
// belongs to.
func ApplyAcceptanceDataToMultiset(ms MultisetWriter, acceptanceData externalapi.AcceptanceData,
	mergingBlockDAAScore uint64) error {
	return forEachAcceptedTransaction(acceptanceData, func(transaction *externalapi.DomainTransaction, isCoinbase bool) error {
		return applyTransactionToMultiset(ms, transaction, mergingBlockDAAScore, isCoinbase, false)
	})
}

// RemoveAcceptanceDataFromMultiset is the exact inverse of ApplyAcceptanceDataToMultiset: it removes
// the created outputs and re-adds the spent inputs, walking acceptanceData in reverse so the net
// effect is an exact undo.
func RemoveAcceptanceDataFromMultiset(ms MultisetWriter, acceptanceData externalapi.AcceptanceData,
	mergingBlockDAAScore uint64) error {
	for i := len(acceptanceData) - 1; i >= 0; i-- {
		blockAcceptanceData := acceptanceData[i]
		for j := len(blockAcceptanceData.TransactionAcceptanceData) - 1; j >= 0; j-- {
			transactionAcceptanceData := blockAcceptanceData.TransactionAcceptanceData[j]
			if !transactionAcceptanceData.IsAccepted {
				continue
			}
			err := applyTransactionToMultiset(ms, transactionAcceptanceData.Transaction,
				mergingBlockDAAScore, j == 0, true)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// ApplyAcceptanceDataToDiff applies every accepted transaction in acceptanceData to diff, stamping
// created entries per AcceptedUTXOBlockDAAScore. It is the UTXODiff counterpart of
// ApplyAcceptanceDataToMultiset and must stay stamped identically to it - the diff and the multiset
// are two representations of the same UTXO set, and a block's own UTXO commitment is only meaningful
// if they agree entry for entry.
func ApplyAcceptanceDataToDiff(diff externalapi.MutableUTXODiff, acceptanceData externalapi.AcceptanceData,
	mergingBlockDAAScore uint64) error {
	return forEachAcceptedTransaction(acceptanceData, func(transaction *externalapi.DomainTransaction, _ bool) error {
		return diff.AddTransaction(transaction, AcceptedUTXOBlockDAAScore(mergingBlockDAAScore))
	})
}

func forEachAcceptedTransaction(acceptanceData externalapi.AcceptanceData,
	apply func(transaction *externalapi.DomainTransaction, isCoinbase bool) error) error {
	for _, blockAcceptanceData := range acceptanceData {
		for i, transactionAcceptanceData := range blockAcceptanceData.TransactionAcceptanceData {
			if !transactionAcceptanceData.IsAccepted {
				continue
			}
			err := apply(transactionAcceptanceData.Transaction, i == 0)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// applyTransactionToMultiset removes the transaction's inputs from ms and adds its outputs, or the
// exact reverse of that when reverse is true.
func applyTransactionToMultiset(ms MultisetWriter, transaction *externalapi.DomainTransaction,
	mergingBlockDAAScore uint64, isCoinbase bool, reverse bool) error {
	transactionID := consensushashing.TransactionID(transaction)

	addUTXO := func(entry externalapi.UTXOEntry, outpoint *externalapi.DomainOutpoint) error {
		serializedUTXO, err := SerializeUTXO(entry, outpoint)
		if err != nil {
			return err
		}
		ms.Add(serializedUTXO)
		return nil
	}
	removeUTXO := func(entry externalapi.UTXOEntry, outpoint *externalapi.DomainOutpoint) error {
		serializedUTXO, err := SerializeUTXO(entry, outpoint)
		if err != nil {
			return err
		}
		ms.Remove(serializedUTXO)
		return nil
	}
	if reverse {
		addUTXO, removeUTXO = removeUTXO, addUTXO
	}

	// Forward order is inputs-then-outputs; reversing an addition means outputs-then-inputs, but the
	// multiset is commutative so only the add/remove direction actually matters here.
	for _, input := range transaction.Inputs {
		err := removeUTXO(input.UTXOEntry, &input.PreviousOutpoint)
		if err != nil {
			return err
		}
	}

	for i, output := range transaction.Outputs {
		if i < 0 || i > math.MaxUint32 {
			return errors.Errorf("output index %d cannot be represented as uint32", i)
		}
		outpoint := &externalapi.DomainOutpoint{
			TransactionID: *transactionID,
			Index:         uint32(i),
		}
		entry := NewUTXOEntry(output.Value, output.ScriptPublicKey,
			isCoinbase, AcceptedUTXOBlockDAAScore(mergingBlockDAAScore))
		err := addUTXO(entry, outpoint)
		if err != nil {
			return err
		}
	}

	return nil
}
