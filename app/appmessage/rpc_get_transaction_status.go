package appmessage

import "github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"

// TransactionStatus describes the current state of a transaction in the node.
type TransactionStatus byte

// The values match protowire.TransactionStatus exactly, and must keep matching. They used to be
// declared in a different order, with Invalid inserted at 3 where the wire enum has ORPHAN, so every
// status from 3 upwards was sent one place out of step: an invalid transaction was reported to
// clients as an orphan, an orphan as accepted, an accepted as confirmed, and a confirmed one as a
// bare 6 the wire enum had no name for. The conversions are explicit now (see
// protowire.toWireTransactionStatus), so a future divergence is a compile error rather than a wallet
// being told a transaction it cannot spend has been accepted.
const (
	TransactionStatusUnknown TransactionStatus = iota
	TransactionStatusNotFound
	TransactionStatusPending
	TransactionStatusOrphan
	TransactionStatusAccepted
	TransactionStatusConfirmed
	TransactionStatusInvalid
)

var transactionStatusToString = map[TransactionStatus]string{
	TransactionStatusUnknown:   "unknown",
	TransactionStatusNotFound:  "not-found",
	TransactionStatusPending:   "pending",
	TransactionStatusOrphan:    "orphan",
	TransactionStatusAccepted:  "accepted",
	TransactionStatusConfirmed: "confirmed",
	TransactionStatusInvalid:   "invalid",
}

func (ts TransactionStatus) String() string {
	statusString, ok := transactionStatusToString[ts]
	if !ok {
		return transactionStatusToString[TransactionStatusUnknown]
	}
	return statusString
}

// GetTransactionStatusRequestMessage is an appmessage corresponding to
// its respective RPC message.
type GetTransactionStatusRequestMessage struct {
	baseMessage
	TransactionID string
}

// Command returns the protocol command string for the message.
func (msg *GetTransactionStatusRequestMessage) Command() MessageCommand {
	return CmdGetTransactionStatusRequestMessage
}

// NewGetTransactionStatusRequestMessage returns an instance of the message.
func NewGetTransactionStatusRequestMessage(transactionID string) *GetTransactionStatusRequestMessage {
	return &GetTransactionStatusRequestMessage{TransactionID: transactionID}
}

// GetTransactionStatusResponseMessage is an appmessage corresponding to
// its respective RPC message.
type GetTransactionStatusResponseMessage struct {
	baseMessage
	Status             TransactionStatus
	Confirmations      uint64
	AcceptingBlockHash *externalapi.DomainHash

	Error *RPCError
}

// Command returns the protocol command string for the message.
func (msg *GetTransactionStatusResponseMessage) Command() MessageCommand {
	return CmdGetTransactionStatusResponseMessage
}

// NewGetTransactionStatusResponseMessage returns an instance of the message.
func NewGetTransactionStatusResponseMessage(status TransactionStatus, acceptingBlockHash *externalapi.DomainHash, confirmations uint64) *GetTransactionStatusResponseMessage {
	return &GetTransactionStatusResponseMessage{Status: status, AcceptingBlockHash: acceptingBlockHash, Confirmations: confirmations}
}
