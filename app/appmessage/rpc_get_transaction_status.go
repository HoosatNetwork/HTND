package appmessage

// TransactionStatus describes the current state of a transaction in the node.
type TransactionStatus byte

const (
	TransactionStatusUnknown TransactionStatus = iota
	TransactionStatusNotFound
	TransactionStatusPending
	TransactionStatusOrphan
	TransactionStatusAccepted
	TransactionStatusConfirmed
)

var transactionStatusToString = map[TransactionStatus]string{
	TransactionStatusUnknown:   "unknown",
	TransactionStatusNotFound:  "not-found",
	TransactionStatusPending:   "pending",
	TransactionStatusOrphan:    "orphan",
	TransactionStatusAccepted:  "accepted",
	TransactionStatusConfirmed: "confirmed",
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
	Status        TransactionStatus
	Confirmations uint64

	Error *RPCError
}

// Command returns the protocol command string for the message.
func (msg *GetTransactionStatusResponseMessage) Command() MessageCommand {
	return CmdGetTransactionStatusResponseMessage
}

// NewGetTransactionStatusResponseMessage returns an instance of the message.
func NewGetTransactionStatusResponseMessage(status TransactionStatus, confirmations uint64) *GetTransactionStatusResponseMessage {
	return &GetTransactionStatusResponseMessage{Status: status, Confirmations: confirmations}
}
