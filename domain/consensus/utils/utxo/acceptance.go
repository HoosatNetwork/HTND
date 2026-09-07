package utxo

import (
	"math"

	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/transactionhelper"
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
//
// selectedParentPastUTXO is the set this block starts from, and it is what keeps the multiset a hash
// of an actual SET. A MuHash Add is not idempotent, so adding one outpoint twice produces a
// value that is not the hash of any set at all - and acceptance data really can present the same
// outpoint twice, because two blocks with byte-identical coinbases share a transaction ID and each
// can be accepted by a different merging block. The diff already refuses the second add
// (mutableUTXODiff.addEntry, "already present in toAdd with the same value"); the multiset had no
// equivalent and hashed it again, so the block's commitment stopped matching its header with nothing
// else about the block wrong. On a live mainnet survey 8,174 coinbase transactions were accepted by
// more than one chain block.
//
// selectedParentPastUTXO may be nil, which keeps the within-replay deduplication but cannot tell that
// a coin already existed before this block.
func ApplyAcceptanceDataToMultiset(ms MultisetWriter, acceptanceData externalapi.AcceptanceData,
	mergingBlockDAAScore uint64, selectedParentPastUTXO externalapi.UTXODiff) error {
	// Two ways the same outpoint can be presented for adding twice, and the diff refuses both:
	//
	//   - it is already in the set before this block, because an earlier chain block accepted the same
	//     byte-identical coinbase. selectedParentPastUTXO's toAdd is where such a coin sits.
	//   - this one acceptance data presents it twice, because two merge-set blocks carried the same
	//     coinbase.
	//
	// Either way the set holds it once, so the multiset must hash it once.
	addedHere := map[externalapi.DomainOutpoint]struct{}{}
	skipDuplicate := func(outpoint *externalapi.DomainOutpoint) bool {
		if _, already := addedHere[*outpoint]; already {
			return true
		}
		if selectedParentPastUTXO != nil && selectedParentPastUTXO.ToAdd().Contains(outpoint) {
			return true
		}
		addedHere[*outpoint] = struct{}{}
		return false
	}

	return forEachAcceptedTransaction(acceptanceData, func(transaction *externalapi.DomainTransaction, isCoinbase bool) error {
		return applyTransactionToMultiset(ms, transaction, mergingBlockDAAScore, isCoinbase, false, skipDuplicate)
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
				mergingBlockDAAScore, IsAcceptedCoinbase(transactionAcceptanceData.Transaction, j), true, nil)
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

// IsAcceptedCoinbase reports whether an accepted transaction is a coinbase, for the purpose of the
// isCoinbase flag stamped into the UTXO entries it creates.
//
// There is one definition and both representations of the UTXO set must use it. isCoinbase is
// serialized into the MuHash preimage by SerializeUTXO, so it is part of a coin's committed identity:
// if the diff and the multiset answer this question differently for the same transaction, they
// produce different bytes for the same coin, the block's commitment stops matching its header, and
// nothing about the block itself is wrong. The diff path has always used the transaction's own
// subnetwork ID via AddTransaction; the multiset path used its position in the block's acceptance
// data instead. Consensus puts the coinbase at index 0, so for a valid block the two agree - but they
// are not the same question, and only one of them is about the transaction.
//
// positionInBlock is kept only so a disagreement can be reported rather than silently chosen between.
func IsAcceptedCoinbase(transaction *externalapi.DomainTransaction, positionInBlock int) bool {
	isCoinbase := transactionhelper.IsCoinBase(transaction)
	if isCoinbase != (positionInBlock == 0) {
		// Either a coinbase somewhere other than index 0, or a non-coinbase at index 0. Both are
		// invalid blocks, so this should be unreachable - and if it is ever reached, it is the exact
		// shape of a commitment failure with no other symptom, which is worth a loud line rather than
		// a silent choice.
		log.Warnf("Transaction %s at position %d of its block: subnetwork says coinbase=%t but position "+
			"says coinbase=%t. Stamping the UTXO entries it creates with the subnetwork's answer, which "+
			"is what the UTXO diff uses. If a block's UTXO commitment fails with nothing else wrong, "+
			"this is why.", consensushashing.TransactionID(transaction), positionInBlock, isCoinbase,
			positionInBlock == 0)
	}
	return isCoinbase
}

func forEachAcceptedTransaction(acceptanceData externalapi.AcceptanceData,
	apply func(transaction *externalapi.DomainTransaction, isCoinbase bool) error) error {
	for _, blockAcceptanceData := range acceptanceData {
		for i, transactionAcceptanceData := range blockAcceptanceData.TransactionAcceptanceData {
			if !transactionAcceptanceData.IsAccepted {
				continue
			}
			err := apply(transactionAcceptanceData.Transaction,
				IsAcceptedCoinbase(transactionAcceptanceData.Transaction, i))
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
	mergingBlockDAAScore uint64, isCoinbase bool, reverse bool,
	skipDuplicate func(*externalapi.DomainOutpoint) bool) error {
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
		if skipDuplicate != nil && skipDuplicate(outpoint) {
			// Already in the set, and the diff refused to add it a second time. Hashing it again here
			// would make the multiset disagree with the very set it is supposed to summarise.
			continue
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
