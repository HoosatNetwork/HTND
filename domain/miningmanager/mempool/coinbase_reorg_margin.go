package mempool

import (
	"fmt"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/consensushashing"
)

// checkCoinbaseReorgSafetyMargin refuses a transaction that spends a coinbase output younger than
// consensus coinbase maturity plus mp.config.CoinbaseReorgSafetyMarginDAAScore, measured against the
// virtual DAA score. It is mempool policy, not consensus: blocks carrying such a transaction stay valid,
// block template building does not call it, and transactions already in blocks are never checked.
//
// A coinbase output exists only while the block paying it stays on the selected chain. When virtual
// reorgs to a sibling chain, the coinbases of the blocks it leaves are not accepted by the new chain and
// their outputs vanish; ordinary transactions from those blocks are merged and survive. Consensus
// maturity is 100 DAA score units on mainnet, about ten seconds, and reorgs that deep happen: on
// 29.9.2026 two chains ran in parallel from DAA 231080602 to about 231080711, and a compound transaction
// (34195391...) spent the coinbase of 0d44a4cf five units past maturity, was mined on the losing chain,
// and was left spending coins that no canonical UTXO set holds. htnwallet waits the same margin
// (coinbaseReorgSafetyMargin in cmd/htnwallet/daemon/server/balance.go); this keeps other wallets'
// transactions of that kind out of this node's mempool, its block templates and its relay.
//
// The transaction's inputs must already be populated from the virtual UTXO set. The rejection uses
// RejectImmatureSpend, which the relay flow does not treat as misbehaviour, so the sending peer is
// neither banned nor disconnected, and the transaction is not relayed further.
func (mp *mempool) checkCoinbaseReorgSafetyMargin(transaction *externalapi.DomainTransaction) error {
	margin := mp.config.CoinbaseReorgSafetyMarginDAAScore
	if margin == 0 || !hasCoinbaseInput(transaction) {
		return nil
	}
	virtualDAAScore, err := mp.consensusReference.Consensus().GetVirtualDAAScore()
	if err != nil {
		return err
	}
	maturity := mp.config.DAGParams.BlockCoinbaseMaturity
	inputIndex, requiredDAAScore, ok := coinbaseInputWithinReorgSafetyMargin(transaction, virtualDAAScore, maturity, margin)
	if !ok {
		return nil
	}
	input := transaction.Inputs[inputIndex]
	return transactionRuleError(RejectImmatureSpend, fmt.Sprintf(
		"transaction %s input #%d spends coinbase output %s:%d from DAA score %d, which this node relays "+
			"only from virtual DAA score %d (coinbase maturity %d plus reorg safety margin %d); "+
			"virtual DAA score is %d",
		consensushashing.TransactionID(transaction), inputIndex,
		input.PreviousOutpoint.TransactionID, input.PreviousOutpoint.Index,
		input.UTXOEntry.BlockDAAScore(), requiredDAAScore, maturity, margin, virtualDAAScore))
}

func hasCoinbaseInput(transaction *externalapi.DomainTransaction) bool {
	for _, input := range transaction.Inputs {
		if input.UTXOEntry != nil && input.UTXOEntry.IsCoinbase() {
			return true
		}
	}
	return false
}

// coinbaseInputWithinReorgSafetyMargin returns the first input spending a coinbase output for which
// virtualDAAScore < BlockDAAScore + coinbaseMaturity + margin, and the virtual DAA score from which that
// input would be accepted. With margin 0 this is exactly consensus's maturity rule.
func coinbaseInputWithinReorgSafetyMargin(transaction *externalapi.DomainTransaction,
	virtualDAAScore, coinbaseMaturity, margin uint64,
) (inputIndex int, requiredDAAScore uint64, found bool) {
	for i, input := range transaction.Inputs {
		entry := input.UTXOEntry
		if entry == nil || !entry.IsCoinbase() {
			continue
		}
		required := entry.BlockDAAScore() + coinbaseMaturity + margin
		if virtualDAAScore < required {
			return i, required, true
		}
	}
	return 0, 0, false
}
