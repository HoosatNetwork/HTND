package testutils

import (
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/testapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/utxo"
	"github.com/HoosatNetwork/HTND/v2/util/staging"
)

// CreateTransactionWithOutput creates a synthetic transaction whose first output pays to opTrue.
// It is intended to be staged directly into the virtual UTXO set for tests.
func CreateTransactionWithOutput(value uint64) *externalapi.DomainTransaction {
	scriptPublicKey, _ := OpTrueScript()

	return &externalapi.DomainTransaction{
		Version: constants.MaxTransactionVersion,
		Inputs:  []*externalapi.DomainTransactionInput{},
		Outputs: []*externalapi.DomainTransactionOutput{{
			Value:           value,
			ScriptPublicKey: scriptPublicKey,
		}},
		Payload: []byte{},
	}
}

// StageTransactionOutputsToVirtual stages the outputs of a synthetic transaction into the current virtual UTXO set.
func StageTransactionOutputsToVirtual(tc testapi.TestConsensus, transaction *externalapi.DomainTransaction, blockDAAScore uint64) error {
	stagingArea := model.NewStagingArea()
	virtualUTXODiff := utxo.NewMutableUTXODiff()
	if err := virtualUTXODiff.AddTransaction(transaction, blockDAAScore); err != nil {
		return err
	}

	tc.ConsensusStateStore().StageVirtualUTXODiff(stagingArea, virtualUTXODiff.ToImmutable())
	return staging.CommitAllChanges(tc.DatabaseContext(), stagingArea)
}

// StageCreatedOutputsToVirtual adds transaction's outputs to virtual and does not spend its inputs.
// The outputs are then UTXOs of the UTXO set, which is what a transaction is allowed to spend.
func StageCreatedOutputsToVirtual(tc testapi.TestConsensus, transaction *externalapi.DomainTransaction, blockDAAScore uint64) error {
	staged := *transaction
	if len(transaction.Inputs) > 0 {
		inputs := make([]*externalapi.DomainTransactionInput, len(transaction.Inputs))
		for i, input := range transaction.Inputs {
			copied := *input
			copied.UTXOEntry = nil
			inputs[i] = &copied
		}
		staged.Inputs = inputs
	}

	stagingArea := model.NewStagingArea()
	virtualUTXODiff := utxo.NewMutableUTXODiff()
	if err := virtualUTXODiff.AddOutputsSpendingResolvedInputs(&staged, blockDAAScore); err != nil {
		return err
	}
	tc.ConsensusStateStore().StageVirtualUTXODiff(stagingArea, virtualUTXODiff.ToImmutable())
	return staging.CommitAllChanges(tc.DatabaseContext(), stagingArea)
}
