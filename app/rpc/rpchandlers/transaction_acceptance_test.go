package rpchandlers

import (
	"testing"

	"github.com/HoosatNetwork/HTND/app/appmessage"

	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/subnetworks"
)

func transactionWithPayload(payload byte) *externalapi.DomainTransaction {
	return &externalapi.DomainTransaction{
		SubnetworkID: subnetworks.SubnetworkIDNative,
		Payload:      []byte{payload},
	}
}

func acceptanceEntry(transaction *externalapi.DomainTransaction, accepted bool,
) *externalapi.TransactionAcceptanceData {
	return &externalapi.TransactionAcceptanceData{Transaction: transaction, IsAccepted: accepted}
}

// TestVerdictBelongsToTheTransactionNotOneCopyOfIt is the bug a live query caught after the first
// version of this fix looked correct - it compiled, had tests, and returned a real accepting block.
//
// A chain block merges every block in its merge set, so when several carry the same transaction its
// acceptance data holds one entry per carrier: exactly one accepted, the rest rejected as duplicates.
// Reading only the entry belonging to whichever copy the block-store scan happened to find reports a
// perfectly good transaction as INVALID - and a wallet told its transaction is invalid may treat the
// funds as lost. On a DAG the scan lands on a duplicate most of the time.
func TestVerdictBelongsToTheTransactionNotOneCopyOfIt(t *testing.T) {
	transaction := transactionWithPayload(1)
	transactionID := consensushashing.TransactionID(transaction)
	other := transactionWithPayload(2)

	// One chain block merged three blocks that all carried this transaction. The first copy was
	// accepted; the other two were rejected as duplicates. The transaction was accepted.
	acceptanceData := externalapi.AcceptanceData{
		{BlockHash: externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{1}),
			TransactionAcceptanceData: []*externalapi.TransactionAcceptanceData{
				acceptanceEntry(other, true), acceptanceEntry(transaction, true)}},
		{BlockHash: externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{2}),
			TransactionAcceptanceData: []*externalapi.TransactionAcceptanceData{
				acceptanceEntry(transaction, false)}},
		{BlockHash: externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{3}),
			TransactionAcceptanceData: []*externalapi.TransactionAcceptanceData{
				acceptanceEntry(transaction, false)}},
	}

	merged, accepted := verdictForTransaction(acceptanceData, transactionID)
	if !merged {
		t.Fatal("the transaction is in this block's acceptance data and must be seen as merged")
	}
	if !accepted {
		t.Error("accepted in one entry and rejected as a duplicate in others means ACCEPTED - " +
			"reporting invalid here tells a wallet its funds are gone")
	}
}

// Merged and rejected everywhere is a settled answer and must not be mistaken for acceptance. This
// is the case the "accepted anywhere" rule must not swallow.
func TestATransactionRejectedEverywhereIsNotAccepted(t *testing.T) {
	transaction := transactionWithPayload(7)
	transactionID := consensushashing.TransactionID(transaction)

	acceptanceData := externalapi.AcceptanceData{
		{BlockHash: externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{1}),
			TransactionAcceptanceData: []*externalapi.TransactionAcceptanceData{
				acceptanceEntry(transaction, false)}},
	}

	merged, accepted := verdictForTransaction(acceptanceData, transactionID)
	if !merged {
		t.Fatal("it is present in the acceptance data, so it was merged")
	}
	if accepted {
		t.Error("every entry rejected it; reporting accepted would be the opposite of the truth")
	}
}

// A transaction no entry mentions has not been merged by this chain block, which is different from
// having been rejected by it - the search has to keep going up the chain rather than settle.
func TestATransactionNotInTheDataIsNotMerged(t *testing.T) {
	present := transactionWithPayload(1)
	absent := transactionWithPayload(9)

	acceptanceData := externalapi.AcceptanceData{
		{BlockHash: externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{1}),
			TransactionAcceptanceData: []*externalapi.TransactionAcceptanceData{
				acceptanceEntry(present, true)}},
	}

	merged, accepted := verdictForTransaction(acceptanceData, consensushashing.TransactionID(absent))
	if merged || accepted {
		t.Errorf("a transaction this block never merged must be reported as neither merged nor "+
			"accepted, got merged=%t accepted=%t", merged, accepted)
	}
}

// TestMalformedTransactionIDIsNotReportedAsNotFound covers a wrong answer that cost real
// investigation time.
//
// A transaction id that lost one character in a copy-paste came back as TRANSACTION_STATUS_NOT_FOUND
// - the same answer the node gives for a transaction it has genuinely never seen. That reads as
// evidence about the network when the truth is about the question, and it sent an investigation after
// transaction propagation instead of after a typo. A request the node cannot parse has to say so.
func TestMalformedTransactionIDIsNotReportedAsNotFound(t *testing.T) {
	tests := []struct {
		name          string
		transactionID string
	}{
		{"one character short, as a truncated copy-paste produces", "eab97296545fcc3a8188e499f8fc4ede1595d5b38b6f9a76801d64dd7c8cd0b"},
		{"empty", ""},
		{"not hexadecimal", "zzzz7296545fcc3a8188e499f8fc4ede1595d5b38b6f9a76801d64dd7c8cd0b7"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// The parse happens before the handler touches the node, so this exercises the real path.
			response, err := HandleGetTransactionStatus(nil, nil,
				appmessage.NewGetTransactionStatusRequestMessage(test.transactionID))
			if err != nil {
				t.Fatalf("a malformed id should be answered, not error out: %+v", err)
			}
			statusResponse := response.(*appmessage.GetTransactionStatusResponseMessage)
			if statusResponse.Status == appmessage.TransactionStatusNotFound {
				t.Error("an id the node cannot parse must not be reported as a transaction it has not " +
					"seen - that is an answer about the network to a question about the request")
			}
			if statusResponse.Error == nil {
				t.Error("the caller has to be told the id was rejected, and why")
			}
		})
	}
}
