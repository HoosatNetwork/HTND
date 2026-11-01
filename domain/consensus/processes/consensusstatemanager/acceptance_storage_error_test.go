package consensusstatemanager

import (
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/utxo"
	"github.com/pkg/errors"
)

var errStorageFault = errors.New("storage fault")

type faultyCoins struct{ model.ConsensusStateStore }

func (faultyCoins) LookupUTXOByOutpoint(model.DBReader, *model.StagingArea, *externalapi.DomainOutpoint) (
	externalapi.UTXOEntry, bool, error,
) {
	return nil, false, errStorageFault
}

// TestAcceptanceReportsStorageErrors checks that a storage error while reading a transaction's
// inputs is returned as an error. It must not become a "missing-input" rejection: that would be
// written into this node's acceptance data and UTXO state as if the transaction broke a rule, so a
// transient local fault would make this node's state differ from every other node's.
func TestAcceptanceReportsStorageErrors(t *testing.T) {
	blockHash := externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{9})
	csm := &consensusStateManager{
		consensusStateStore:  faultyCoins{},
		transactionValidator: acceptingTransactionValidator{},
		pruningStore:         noPruningPointStore{},
		ghostdagDataStore:    missingGHOSTDAGDataStore{},
	}
	tx := &externalapi.DomainTransaction{Inputs: []*externalapi.DomainTransactionInput{{PreviousOutpoint: *outpoint(3, 0)}}}

	accepted, _, rejection, err := csm.maybeAcceptTransaction(model.NewStagingArea(), tx, blockHash, blockHash,
		false, utxo.NewMutableUTXODiff(), make(map[externalapi.DomainOutpoint]struct{}), 0, 0, 1)
	if !errors.Is(err, errStorageFault) {
		t.Fatalf("got accepted=%t rejection=%+v err=%v; want the storage error returned", accepted, rejection, err)
	}
}
