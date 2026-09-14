package rpchandlers

import (
	"testing"

	"github.com/HoosatNetwork/HTND/app/appmessage"
	"github.com/HoosatNetwork/HTND/app/rpc/rpccontext"
	"github.com/HoosatNetwork/HTND/domain"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/domain/miningmanager"
)

type mempoolEntryMiningManager struct {
	miningmanager.MiningManager
	transaction *externalapi.DomainTransaction
}

func (m mempoolEntryMiningManager) GetTransactionNoClone(transactionID *externalapi.DomainTransactionID,
	includeTransactionPool bool, includeOrphanPool bool,
) (*externalapi.DomainTransaction, bool, bool) {
	if includeTransactionPool && consensushashing.TransactionID(m.transaction).Equal(transactionID) {
		return m.transaction, false, true
	}
	return nil, false, false
}

type mempoolEntryDomain struct {
	domain.Domain
	miningManager miningmanager.MiningManager
}

func (d mempoolEntryDomain) MiningManager() miningmanager.MiningManager { return d.miningManager }

// TestHandleGetMempoolEntryReportsFee pins that the entry carries the fee of the transaction that
// was looked up, not of an empty placeholder.
func TestHandleGetMempoolEntryReportsFee(t *testing.T) {
	transaction := &externalapi.DomainTransaction{
		Inputs:  []*externalapi.DomainTransactionInput{},
		Outputs: []*externalapi.DomainTransactionOutput{},
		Payload: []byte{},
		Fee:     1234,
		Mass:    1,
	}
	context := &rpccontext.Context{
		Domain: mempoolEntryDomain{miningManager: mempoolEntryMiningManager{transaction: transaction}},
	}

	request := &appmessage.GetMempoolEntryRequestMessage{TxID: consensushashing.TransactionID(transaction).String()}
	response, err := HandleGetMempoolEntry(context, nil, request)
	if err != nil {
		t.Fatalf("HandleGetMempoolEntry: %+v", err)
	}
	entryResponse := response.(*appmessage.GetMempoolEntryResponseMessage)
	if entryResponse.Error != nil {
		t.Fatalf("unexpected RPC error: %s", entryResponse.Error.Message)
	}
	if entryResponse.Entry.Fee != transaction.LoadFee() {
		t.Fatalf("expected fee %d, got %d", transaction.LoadFee(), entryResponse.Entry.Fee)
	}
}
