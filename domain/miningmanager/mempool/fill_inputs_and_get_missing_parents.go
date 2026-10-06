package mempool

import (
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/ruleerrors"
	"github.com/HoosatNetwork/HTND/v2/domain/miningmanager/mempool/model"
	"github.com/pkg/errors"
)

func (mp *mempool) fillInputsAndGetMissingParents(transaction *externalapi.DomainTransaction) (
	parents model.IDToTransactionMap, missingOutpoints []*externalapi.DomainOutpoint, err error,
) {
	parentsInPool := mp.transactionsPool.getParentTransactionsInPool(transaction)

	// Do not attach outputs of a mempool parent. Those outputs are not UTXOs of a UTXO-valid block.
	// Consensus resolves each input from virtual, and a miss leaves the transaction an orphan until
	// a UTXO-valid block has accepted the output.
	err = mp.consensusReference.Consensus().ValidateTransactionAndPopulateWithConsensusData(transaction)
	if err != nil {
		errMissingOutpoints := ruleerrors.ErrMissingTxOut{}
		if errors.As(err, &errMissingOutpoints) {
			return parentsInPool, errMissingOutpoints.MissingOutpoints, nil
		}
		if errors.Is(err, ruleerrors.ErrImmatureSpend) {
			return nil, nil, transactionRuleError(
				RejectImmatureSpend, "one of the transaction inputs spends an immature UTXO")
		}
		if errors.As(err, &ruleerrors.RuleError{}) {
			return nil, nil, newRuleError(err)
		}
		return nil, nil, err
	}

	return parentsInPool, nil, nil
}
