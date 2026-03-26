package testutils

import (
	"github.com/Hoosat-Oy/HTND/domain/consensus/model"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/testapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/constants"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/utxo"
	"github.com/Hoosat-Oy/HTND/util/staging"
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
