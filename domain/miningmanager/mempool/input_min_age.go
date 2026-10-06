package mempool

import (
	"fmt"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/consensushashing"
)

// checkInputMinAge refuses a transaction any of whose inputs is younger than
// mp.config.InputMinAgeDAAScore, measured against the virtual DAA score. A coinbase input must also
// be past consensus coinbase maturity first:
//
//	non-coinbase input: refused while virtualDAAScore < BlockDAAScore + InputMinAgeDAAScore
//	coinbase input:     refused while virtualDAAScore < BlockDAAScore + BlockCoinbaseMaturity + InputMinAgeDAAScore
//
// It is mempool policy, not consensus: blocks carrying such a transaction stay valid, block template
// building does not call it, and transactions already in blocks are never checked.
//
// A coin exists only while the chain that accepted it stays selected. When virtual reorgs to a
// sibling chain, coinbase outputs of the blocks it leaves vanish outright, and so does every output
// of a transaction that spent them - and, where a node accepts a transaction despite missing inputs,
// whatever that transaction created. On 29.9.2026 two chains ran in parallel from DAA 231080602 to
// about 231080711, and compound transaction 34195391... spent the coinbase of 0d44a4cf five units past
// maturity, was mined on the losing chain, and was left spending coins no canonical UTXO set holds.
// Requiring every input to be InputMinAgeDAAScore old keeps the coins this node pools, relays and
// offers to miners well below the depth of any reorg seen. htnwallet selects coins by the same rule.
//
// The inputs must already be populated from the virtual UTXO set. The refusal is RejectImmatureSpend,
// which the relay flow does not treat as misbehaviour: the sending peer is neither banned nor
// disconnected, and the transaction is not relayed.
func (mp *mempool) checkInputMinAge(transaction *externalapi.DomainTransaction) error {
	minAge := mp.config.InputMinAgeDAAScore
	if minAge == 0 {
		return nil
	}
	virtualDAAScore, err := mp.consensusReference.Consensus().GetVirtualDAAScore()
	if err != nil {
		return err
	}
	maturity := mp.config.DAGParams.BlockCoinbaseMaturity
	inputIndex, requiredDAAScore, found := inputBelowMinAge(transaction, virtualDAAScore, maturity, minAge)
	if !found {
		return nil
	}
	input := transaction.Inputs[inputIndex]
	if input.UTXOEntry == nil {
		return mp.inputsWithoutUTXOEntryError(transaction, []*externalapi.DomainOutpoint{&input.PreviousOutpoint})
	}
	rule := fmt.Sprintf("minimum input age %d", minAge)
	if input.UTXOEntry.IsCoinbase() {
		rule = fmt.Sprintf("coinbase maturity %d plus minimum input age %d", maturity, minAge)
	}
	return transactionRuleError(RejectImmatureSpend, fmt.Sprintf(
		"transaction %s input #%d spends %s:%d from DAA score %d, which this node accepts and relays only "+
			"from virtual DAA score %d (%s); virtual DAA score is %d",
		consensushashing.TransactionID(transaction), inputIndex,
		input.PreviousOutpoint.TransactionID, input.PreviousOutpoint.Index,
		input.UTXOEntry.BlockDAAScore(), requiredDAAScore, rule, virtualDAAScore))
}

// inputsWithoutUTXOEntryError refuses a transaction spending outputs that are not in the virtual UTXO
// set: outputs of transactions still in the mempool, or of transactions this node has not seen mined.
// Such an input is younger than any minimum age, so with InputMinAgeDAAScore > 0 the transaction is
// refused rather than held as an orphan - holding it would only defer the same refusal to the moment
// its parents are mined, when their outputs are brand new.
func (mp *mempool) inputsWithoutUTXOEntryError(transaction *externalapi.DomainTransaction,
	missingOutpoints []*externalapi.DomainOutpoint,
) error {
	return transactionRuleError(RejectImmatureSpend, fmt.Sprintf(
		"transaction %s spends %d output(s) not in the virtual UTXO set (%s): outputs of unconfirmed or "+
			"unknown transactions, which this node does not accept or relay; every input must be at least "+
			"%d DAA score units old",
		consensushashing.TransactionID(transaction), len(missingOutpoints), formatOutpoints(missingOutpoints),
		mp.config.InputMinAgeDAAScore))
}

// inputBelowMinAge returns the first input that is younger than the minimum age at virtualDAAScore
// (an input without a UTXO entry counts as younger than any age), and the virtual DAA score from which
// that input would be accepted. With minAge 0 a coinbase input is held to consensus maturity only and
// any other input passes.
func inputBelowMinAge(transaction *externalapi.DomainTransaction,
	virtualDAAScore, coinbaseMaturity, minAge uint64,
) (inputIndex int, requiredDAAScore uint64, found bool) {
	for i, input := range transaction.Inputs {
		entry := input.UTXOEntry
		if entry == nil {
			return i, 0, true
		}
		required := entry.BlockDAAScore() + minAge
		if entry.IsCoinbase() {
			required += coinbaseMaturity
		}
		if virtualDAAScore < required {
			return i, required, true
		}
	}
	return 0, 0, false
}
