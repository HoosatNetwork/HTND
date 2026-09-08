package consensusstatemanager

import (
	"fmt"

	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/utxo"
)

// blockOnlyCarriesTheInheritedOffset reports whether a block's UTXO commitment mismatch is fully
// explained by the offset it inherited, rather than by anything the block's own resolution did
// wrong.
//
// This is what makes an incorrect baseline survivable instead of blinding. A node built on an
// incomplete pruning-point UTXO set cannot reproduce any block's commitment - MuHash is homomorphic,
// so the offset reaches every descendant unchanged, and the hash of a wrong set is wrong no matter
// how correct the arithmetic on top of it is. The node therefore tolerates the mismatch and keeps
// going. But "tolerate the commitment mismatch" was implemented as "tolerate everything", which also
// tolerates a block whose resolution is genuinely broken: new corruption arriving on top of the old
// is waved through with exactly the same log line.
//
// The two are distinguishable without knowing the true set. A block's acceptance data says which
// coins it creates and destroys, and its UTXO diff is the other representation of the same thing. If
// the two agree element by element, the block's own arithmetic is correct and its commitment is
// wrong only because the set it started from was - the definition of carrying the offset. If they
// disagree, the block created or destroyed something its own acceptance data does not account for,
// which no baseline offset can explain and which must not be tolerated.
//
// daaScore is the merging block's own DAA score, the score every entry it creates is stamped with -
// see utxo.AcceptedUTXOBlockDAAScore.
// lookupVirtual resolves an outpoint in virtual's materialised UTXO table. Injected rather than
// reached through the manager so this stays a pure decision that can be tested for each of the
// verdicts it makes.
func blockOnlyCarriesTheInheritedOffset(acceptanceData externalapi.AcceptanceData,
	pastUTXODiff externalapi.UTXODiff, daaScore uint64,
	lookupVirtual func(*externalapi.DomainOutpoint) (externalapi.UTXOEntry, bool),
) (bool, string) {
	if pastUTXODiff == nil {
		// Nothing to check against; assume the block is only carrying the offset rather than
		// disqualifying it on missing information.
		return true, ""
	}

	// pastUTXODiff is a diff from VIRTUAL to this block's past, so a coin's absence from toAdd means
	// nothing on its own: a coin both virtual and this block hold needs no diff entry at all. The
	// question is whether the coin is in the block's past VIEW, which is virtual amended by the diff.
	// Testing toAdd membership instead makes the verdict depend on where virtual happens to be, and
	// convicts a perfectly good block as soon as virtual advances past it.
	inPastView := func(outpoint *externalapi.DomainOutpoint) (externalapi.UTXOEntry, bool) {
		if entry, ok := pastUTXODiff.ToAdd().Get(outpoint); ok {
			return entry, true
		}
		if pastUTXODiff.ToRemove().Contains(outpoint) {
			return nil, false
		}
		if lookupVirtual == nil {
			return nil, false
		}
		return lookupVirtual(outpoint)
	}

	// Outpoints that this same past both creates and spends. Such a coin nets to nothing and leaves
	// no trace in the diff at all: mutableUTXODiff.removeEntry cancels the pending toAdd rather than
	// recording a removal, so the coin appears in neither toAdd nor toRemove, and it was never in
	// virtual either. Without this set, every block carrying a transaction that spends another
	// transaction merged alongside it is convicted of losing a coin it never lost - and a chain of
	// compounding transactions is exactly that shape.
	spentInThisPast := make(map[externalapi.DomainOutpoint]struct{})
	for _, blockAcceptanceData := range acceptanceData {
		for _, transactionAcceptance := range blockAcceptanceData.TransactionAcceptanceData {
			if !transactionAcceptance.IsAccepted {
				continue
			}
			for _, input := range transactionAcceptance.Transaction.Inputs {
				spentInThisPast[input.PreviousOutpoint] = struct{}{}
			}
		}
	}

	for _, blockAcceptanceData := range acceptanceData {
		for i, transactionAcceptance := range blockAcceptanceData.TransactionAcceptanceData {
			if !transactionAcceptance.IsAccepted {
				continue
			}
			transaction := transactionAcceptance.Transaction
			transactionID := consensushashing.TransactionID(transaction)
			isCoinbase := utxo.IsAcceptedCoinbase(transaction, i)

			for outputIndex, output := range transaction.Outputs {
				outpoint := &externalapi.DomainOutpoint{
					TransactionID: *transactionID,
					Index:         uint32(outputIndex),
				}
				entry, present := inPastView(outpoint)
				if !present {
					// Absent from the block's past view entirely. Correct only when this same past also
					// spent it - created and destroyed, netting to nothing, which the multiset nets to
					// nothing as well.
					if pastUTXODiff.ToRemove().Contains(outpoint) {
						continue
					}
					if _, spentHere := spentInThisPast[*outpoint]; spentHere {
						// Created and destroyed inside this same past. It nets to nothing in the diff
						// and nets to nothing in the multiset, so its absence is correct.
						continue
					}
					return false, fmt.Sprintf("accepted transaction %s output %d is absent from the "+
						"block's own past UTXO set", transactionID, outputIndex)
				}
				expected := utxo.NewUTXOEntry(output.Value, output.ScriptPublicKey, isCoinbase,
					utxo.AcceptedUTXOBlockDAAScore(daaScore))
				if !entry.Equal(expected) {
					return false, fmt.Sprintf("accepted transaction %s output %d is in the block's past "+
						"UTXO set with different contents than its acceptance data describes", transactionID,
						outputIndex)
				}
			}
		}
	}
	return true, ""
}
