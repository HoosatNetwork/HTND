package consensusstatemanager

import (
	"math/big"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/blockheader"
	"github.com/pkg/errors"
)

var errInjectedDatabaseFailure = errors.New("injected database failure")

type failingUTXOLookupStore struct{ model.ConsensusStateStore }

func (failingUTXOLookupStore) HasUTXOByOutpoint(model.DBReader, *model.StagingArea, *externalapi.DomainOutpoint) (bool, error) {
	return false, errInjectedDatabaseFailure
}

type noPruningPointStore struct{ model.PruningStore }

func (noPruningPointStore) HasPruningPoint(model.DBReader, *model.StagingArea) (bool, error) {
	return false, nil
}

type missingGHOSTDAGDataStore struct{ model.GHOSTDAGDataStore }

func (missingGHOSTDAGDataStore) Get(model.DBReader, *model.StagingArea, *externalapi.DomainHash, bool) (*externalapi.BlockGHOSTDAGData, error) {
	return nil, errors.New("no GHOSTDAG data")
}

type fixedPastMedianTimeManager struct{ model.PastMedianTimeManager }

func (fixedPastMedianTimeManager) PastMedianTime(*model.StagingArea, *externalapi.DomainHash) (int64, error) {
	return 0, nil
}

// TestBlockTransactionValidationReportsInputLookupFailures pins that a failure to look up a transaction's inputs that is
// not a missing input - a database read error - fails the block's transaction validation. It used to be dropped: the
// transaction's inputs were never validated, the function returned nil, and the block was stored as UTXO-valid, so a
// transient local fault left a durable status that differs from every other node.
func TestBlockTransactionValidationReportsInputLookupFailures(t *testing.T) {
	csm := &consensusStateManager{
		consensusStateStore:   failingUTXOLookupStore{},
		pruningStore:          noPruningPointStore{},
		ghostdagDataStore:     missingGHOSTDAGDataStore{},
		pastMedianTimeManager: fixedPastMedianTimeManager{},
	}

	header := blockheader.NewImmutableBlockHeader(1, nil, &externalapi.DomainHash{}, &externalapi.DomainHash{},
		&externalapi.DomainHash{}, 0, 0, 0, 1, 0, big.NewInt(0), &externalapi.DomainHash{})
	coinbase := &externalapi.DomainTransaction{}
	spending := &externalapi.DomainTransaction{
		Inputs: []*externalapi.DomainTransactionInput{{
			PreviousOutpoint: externalapi.DomainOutpoint{TransactionID: externalapi.DomainTransactionID{}, Index: 0},
		}},
	}
	block := &externalapi.DomainBlock{Header: header, Transactions: []*externalapi.DomainTransaction{coinbase, spending}}

	err := csm.validateBlockTransactionsAgainstPastUTXO(model.NewStagingArea(), block, nil, nil, nil)
	if !errors.Is(err, errInjectedDatabaseFailure) {
		t.Fatalf("expected the input lookup failure to fail validation, got %v", err)
	}
}
